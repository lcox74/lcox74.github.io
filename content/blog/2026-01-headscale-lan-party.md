---
title: "Headscale LAN Party"
date: 2026-01-03
tags: [homelab, proxmox, lxc, networking]
summary: "Running my own Tailscale so my mates can't blame the network when they lose"
---

LAN parties. Remember those?  Back when I had friends we used to smash out 
countless hours of Battlefield 2, Age of Empires, and Halo CE. The chaos of
trying to set everyone up in the house so we couldn't see each other's screens
and the hours of trying to get the multiplayer to bloody work to begin with was
the real character of local multi-player. Skip forward a few years and we used 
LogMeIn Hamachi to simulate LAN parties when we couldn't all get together as 
long as we limited the number of players. 

Enter Headscale: a self-hosted implementation of the Tailscale control server.
It lets you create your own mesh VPN where all your devices (and your mates' 
devices) appear on the same virtual network. This way we can have a LAN with
no need for port forwarding, no subscriptions only maintenance.

> I'm not using Tailscale because I wanted to play with Headscale.

## The Setup

Headscale runs on my Proxmox cluster as an LXC, similar to my Plex setup. It
sits in my Public Services `VLAN 22` and is exposed to the internet through a
reverse proxy with a valid TLS certificate. Tailscale clients are picky about
HTTPS.

> Even though Headscale is behind a reverse proxy, I keep it in the public
> services VLAN rather than internal services. External hosts connect to it,
> so if it gets compromised I'd rather the attacker land in my DMZ than
> somewhere with more internal access.

The rough architecture looks like this:

```txt
Internet
    |
[ Reverse Proxy ] -> https://hs.lachlancox.dev
    |
[ Headscale LXC ] (coordination only, no traffic)
    |
    +-- My devices
    +-- Mate's devices
    +-- Game servers
```

The important thing to understand is that headscale only handles coordination. 
Once devices are connected, traffic flows directly between them (peer-to-peer)
or through DERP relays if direct connections fail. 

## Creating the LXC

Similar to the Plex LXC server setup, I'm going to be running it on my 
`px-jeddah` host and I'll create it using the Proxmox CLI. We are using 
Debian for an actual reason this time, the official release of headscale is
DEB packages so we might as well use Debian.

```bash
# First we need to get the next container id (CT ID)
CTID=$(pvesh get /cluster/nextid)

# Now we can create the LXC
pct create "$CTID" "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst" \
    --hostname "svc-headscale" \
    --cores "1" \
    --memory "1024" \
    --swap "0" \
    --rootfs "local-lvm:4" \
    --net0 "name=eth0,bridge=vmbr0,ip=192.168.33.15/24,gw=192.168.33.1,tag=22"\
    --unprivileged "1" \
    --onboot 1

# Start it up
pct start "$CTID"
```

## Installing Headscale

As mentioned before Headscale provides an official DEB package. This can be 
downloaded from their [website](https://headscale.net/stable/) and installed
following the docs they supply (yeah shock).

```bash
pct exec "$CTID" -- bash -eux  <<'EOF'

export DEBIAN_FRONTEND=noninteractive

apt update
apt install -y --no-install-recommends \
  ca-certificates \
  locales \
  wget \
  yq

# Ensure locales are sane (avoids perl/apt warnings)
sed -i 's/^# en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen
locale-gen
update-locale LANG=en_US.UTF-8

apt upgrade -y

# Download headscale to a safe location
export HEADSCALE_VER=0.27.1
export DEB=/tmp/headscale_${HEADSCALE_VER}.deb

wget -O "$DEB" \
  "https://github.com/juanfont/headscale/releases/download/v${HEADSCALE_VER}/headscale_${HEADSCALE_VER}_linux_amd64.deb"

# Install headscale
apt install -y "$DEB"

# Start it up
systemctl enable --now headscale

EOF
```

## Configuration

Getting the basic configuration setup is quite simple, you look at the config 
that was generated and make the changes marked as required for production. 
Fortunately, there are only 2 things we care about.

```bash
pct exec "$CTID" -- bash -eux  <<EOF

# Patch the config
yq -yi '
  .server_url = "https://hs.lachlancox.dev" |
  .listen_addr = "0.0.0.0:8080"
' /etc/headscale/config.yaml

# Run the new headscale config
systemctl restart headscale

EOF
```

Before we create users and use it, we should probably setup the reverse proxy.
I have a LXC reverse proxy running [caddy](https://caddyserver.com/) called
`infra-proxy` that sits in my `VLAN 22`. I might go over it in another post at 
some point but the following is what I need for Headscale:

```caddyfile
(cloudflare-tls) {
    email {env.CLOUDFLARE_EMAIL}
    tls {
        dns cloudflare {env.CLOUDFLARE_API_TOKEN}
    }
}

# Headscale Control Server (svc-headscale LXC: 192.168.33.15)
hs.lachlancox.dev {
    import cloudflare-tls
    reverse_proxy 192.168.33.15:8080 
}
```

## Creating Users

Headscale uses "users" to group devices. I'm creating one for myself, one for 
game servers, and one for each mate. This keeps things organised and makes it
easy to revoke access later if needed when they beat me too much.

```bash
pct exec "$CTID" -- bash -eux <<EOF

# Create users
headscale users create lachlan
headscale users create gameservers
headscale users create jimmy
headscale users create danie

EOF
```

To actually connect devices, you need auth keys. Pre-auth keys let devices
register without manual approval, which is what you want when you're trying
to get everyone connected for a game.

```bash
# List users to get their IDs
pct exec "$CTID" -- headscale users list

# Generate a pre-auth key for jimmy (expires in 24h)
# Use the numeric ID from the users list (e.g., 3 for jimmy)
pct exec "$CTID" -- headscale preauthkeys create --user 3 --expiration 24h
```

This spits out a key that Jimmy can use to connect. On his machine, he just
needs to install the Tailscale client and run:

```bash
tailscale up --login-server https://hs.lachlancox.dev --authkey <the-key-you-gave-them>
```

Once connected, you can verify everything is working:

```bash
pct exec "$CTID" -- headscale nodes list
```

You should see all the connected devices, their IPs on the tailnet, and which
user they belong to.

## Access Control Lists

Well now that we have this working. I would like to have it actually locked
down a bit so that Danie doesn't just port scan my whole network and start 
mining Ethereum on my hardware. Fortunately, headscale does support ACLs but 
this will need to have a setting changed in the headscale config. But before
we change that we should create the policy file:

```bash
pct exec "$CTID" -- tee /etc/headscale/acl.json <<'EOF'
{
  "groups": {
    "group:admins": ["lachlan"],
    "group:players": ["jimmy", "danie"]
  },
  "hosts": {
    "minecraft": "100.64.0.10",
    "halo": "100.64.0.11"
  },
  "acls": [
    {
      "action": "accept",
      "src": ["group:admins"],
      "dst": ["gameservers:*"]
    },
    {
      "action": "accept",
      "src": ["group:players"],
      "dst": ["minecraft:25565", "halo:2302-2303"]
    }
  ]
}
EOF
```

This policy does a few things:

- The `hosts` section defines specific game servers by their tailnet IPs
- I'm in `group:admins` and can access any port on `gameservers` devices
- Jimmy and Danie are in `group:players` and can only reach `minecraft` on
  port `25565` and `halo` on ports `2302-2303`
- Everything else is implicitly denied, so players can't reach each other or
  any of my other devices
- Or, it could completely break everything and need me to revert back to no
  ACLs.

Now tell headscale to use it by updating the config:

```bash
pct exec "$CTID" -- bash -eux <<EOF

yq -yi '.policy.path = "/etc/headscale/acl.json"' /etc/headscale/config.yaml

systemctl restart headscale

EOF
```

You can test the policy is loaded by checking the logs or just trying to ping
between devices. Jimmy shouldn't be able to reach Danie, but both should be
able to connect to a Minecraft server running under the `gameservers` user.

## What's Next

And that's it. I now have my own Tailscale-compatible network that I control
entirely. No subscriptions, no device limits, just a single LXC quietly
coordinating connections between devices.

The real test will be the next time someone suggests a game night. Instead of
spending an hour trying to remember which ports Minecraft needs forwarded, I
can just generate a key and send it over. Five minutes of setup, assuming they
can follow instructions, and then an hour of debugging. We'll see.

I'm also contemplating whether I should integrate authorisation as something 
that could be done by my discord server. This would require a bit of effort 
but would simplify adding people and setting up new games.

Playing with DERP would also be interesting as a later feature, or I might 
just be unhinged enough to attempt to make my own tailscale like service.

Of course, the irony is that I've spent more time setting this up than I have
actually playing games with anyone. But at least when that LAN party finally
happens, I'll be ready.
