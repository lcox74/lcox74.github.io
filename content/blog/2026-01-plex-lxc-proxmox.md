---
title: "Running Plex in an LXC on Proxmox"
date: 2026-01-02
tags: [homelab, proxmox, lxc, plex]
summary: "Got Plex running in an LXC with iGPU passthrough. Now I maintain media infrastructure for family members. This is fine."
---

In the age of everything being on a streaming subscription you end up sinking
a lot of money a month watching shows you've watched hundreds of times. It 
mildly annoys me enough that I wanted to digitise the media I have and self
host a media server so I can easily access them as though it was another 
streaming service.

I actually did this years ago and have been collecting this whole time but my 
homelab has been completely busted for months running work-related services.
At this point, I just want my Plex back so I can continue watching old episodes
of "Top Gear" and "Good Game" on repeat like some kind of sicko.

At some point I'll need to fix up my sonarr, radarr, and lidarr instances
so I can properly organise and manage my media, but that is a later problem I
want to watch Clarkson burning a caravan now.

## The Hardware

This Plex instance runs on a fairly standard multi-node Proxmox setup built 
from a couple of small form factor PCs. I’m not running High Availability (HA)
for Plex yet, although I might revisit that later.

The specific node hosting Plex is my `px-jeddah` host: a 
**Lenovo ThinkCentre M70q Gen 6 Tiny**  with an Intel Ultra 7 265T CPU, 64GB 
of RAM, and a mix of 512GB and 4TB NVMe storage. The important part here is 
the Intel iGPU, which is what Plex will use for hardware transcoding. Yes this
is overkill and I will only be using a tenth of its resources.

I’ve also removed the onboard Wi-Fi card and replaced it with a 2.5GbE NIC. 
This required a bit of physical persuasion, but it should keep the box useful 
for a long time.

I don’t store any media on the Proxmox hosts themselves. All media lives on a 
separate NAS, which exports a Network File System (NFS) share that the Plex 
server can mount.

## Network Layout

My network is built around UniFi gear with multiple VLANs to keep things
segmented. Plex runs in VLAN 22, which I treat as a public services / DMZ 
network. This VLAN does not have access to the rest of my internal network, 
with the exception of the NAS, which is explicitly allowed through firewall 
rules.

Because Plex runs on a different VLAN, it also lives on a different subnet 
(for example `192.168.33.0/24`) compared to my internal user network 
(`10.10.20.0/24`). This is worth calling out because adopting a Plex server 
requires the client and server to be on the same subnet. If you’re not using 
VLANs, this won’t matter. If you are, it can be a surprisingly annoying gotcha.

## Why an LXC?

I've ran Plex in a full VM and in Docker before but I find running it as an 
LXC a lot lighter and easier to work with. Setting up iGPU passthrough on a LXC
is quite trivial and it starts up a lot faster than the fully isolated VM does.
I can't be bothered dealing with docker when I can just run it directly on the
LXC as the only service.

## Media Storage

As mentioned earlier, all media is stored on a NAS and exported over NFS. In 
this setup, the NFS share is mounted directly inside the container.

This means the container needs a fixed IP address so I can grant it read/write
permissions on the NAS. Ideally Plex would be read-only, but it does write 
metadata alongside media, and that makes the overall experience nicer. I might
revisit this later, but it’s good enough for now.

## Creating the LXC

At a high level, the setup looks like this:

1. Create the LXC
2. Enable iGPU passthrough
3. Mount the NFS share
4. Install Plex
5. Adopt the server

The container is privileged because I want to mount NFS directly inside it. 
I’m creating everything via the Proxmox CLI on the `px-jeddah` host, with the 
intention of eventually scripting this for repeatable deployments.

I’m using a Debian 13 server template purely out of habit. First, we grab the 
next available container ID and create the LXC:


```bash
# First we need to get the next container id (CT ID)
CTID=$(pvesh get /cluster/nextid)

# Now we can create the LXC
pct create "$CTID" "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst" \
    --hostname "svc-plex" \
    --cores "4" \
    --memory "2048" \
    --swap "0" \
    --rootfs "local-lvm:16" \
    --net0 "name=eth0,bridge=vmbr0,ip=10.10.20.132/24,gw=10.10.20.1,tag=2"\
    --unprivileged "0" \
    --features "nesting=0,mount=nfs"\
    --tags "igpu" \
    --onboot 1

# Update and Install Dependencies
pct exec "${CTID}" -- bash -eux <<EOF

export DEBIAN_FRONTEND=noninteractive

apt update
apt install -y --no-install-recommends \
  ca-certificates \
  locales \
  wget 

# Ensure locales are sane (avoids perl/apt warnings)
sed -i 's/^# en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen
locale-gen
update-locale LANG=en_US.UTF-8

# Update the System
apt upgrade -y

# Install dependencies
apt install -y curl gnupg nfs-common

EOF
```

## iGPU Passthrough

Because this host uses an Intel CPU, Plex will use Quick Sync via VAAPI for 
hardware transcoding. That requires access to the DRM render node, 
specifically `/dev/dri/renderD128`.

On the Proxmox host, we can inspect the device to find its major and minor 
numbers:

```bash
ls -l /dev/dri
```

You should see something like:

```txt
crw-rw---- 1 root render 226, 128 renderD128
```

With that information, we can grant the container access to the device and 
bind-mount `/dev/dri`:

```bash
# Stop the LXC if it is running
pct stop ${CTID}

# Add the mounts and permissions to the LXC config
echo "lxc.cgroup2.devices.allow: c 226:128 rwm" >> /etc/pve/lxc/${CTID}.conf
echo "lxc.mount.entry: /dev/dri dev/dri none bind,optional,create=dir" >> /etc/pve/lxc/${CTID}.conf

# Start the LXC back up
pct start ${CTID}
```

In practice, you generally want only one container actively using the iGPU on 
a given host. Trying to share it tends to end in pure chaos.

## Mounting the NFS Share

The Plex container has been granted read/write access to the 
`/var/nfs/shared/Media` share on the NAS. To mount it, we add an entry to 
`/etc/fstab` inside the container.

I’m executing this from the Proxmox host for convenience, but you could just 
as easily shell into the container and do it there:

```bash
pct exec "${CTID}" -- bash -s "nfs.home.lachlancox.dev" "/var/nfs/shared/Media" "/mnt/media" <<EOF
set -euo pipefail

NFS_SERVER="$1"
NFS_SHARE="$2"
NFS_MOUNT="$3"

# Create the mounting point
mkdir -p "$NFS_MOUNT"

# Add the fstab entry to mount
echo "$NFS_SERVER:$NFS_SHARE $NFS_MOUNT nfs defaults,_netdev,bg 0 0" >> /etc/fstab

# Trigger Mount
mount -a

EOF
```

## Actually Install Plex

With the plumbing in place, installing Plex itself is refreshingly boring. 
I’m using the official Plex repository rather than manually installing a 
`.deb`, which makes upgrades straightforward.

```bash
pct exec "${CTID}" -- bash -eux <<EOF

# Add Plex repository GPG key
curl -fsSL https://downloads.plex.tv/plex-keys/PlexSign.key | gpg --dearmor -o /usr/share/keyrings/plex-archive-keyring.gpg

# Add Plex repository
echo "deb [signed-by=/usr/share/keyrings/plex-archive-keyring.gpg] https://downloads.plex.tv/repo/deb public main" > /etc/apt/sources.list.d/plexmediaserver.list

# Install Plex Media Server
apt update
apt install -y plexmediaserver

EOF
```

From this point on, updating Plex is just a normal `apt upgrade`.

## Adopt the Plex Server

Once installed, Plex is available at `http://10.10.20.132:32400/web`. Signing 
in and adopting the server walks you through the usual setup wizard.

For media paths, everything lives under `/mnt/media`. Hardware transcoding 
should be detected automatically, assuming the iGPU passthrough is working 
correctly.

After adoption, I moved the service onto `VLAN 22` and updated the NAS to 
allow access from the new `192.168.33.10/24` address. The final step was 
forwarding `tcp/udp 32400` port to allow remote access.


## What's Next

I now have my `svc-plex` back, this brings me joy and I can already see my 
brother-in-law binge watching Mrs Browns Boys. At some point I'll fix up 
Sonarr, Radarr, and Lidarr again but this is a problem for future-me.

Of course, the punchline is that I'm probably still sinking a lot of money 
every month into streaming subscriptions and now I get the added joy of 
maintaining a Plex server for family users. Now suddenly Plex outages matter.

