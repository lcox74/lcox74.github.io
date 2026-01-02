---
title: 'Homelab'
description: 'My homelab setup - hardware, network, and services'
---

What started as a Plex server has slowly grown into a full home infrastructure
setup. I also use it as a testing ground for work - we run Proxmox internally,
so having my own cluster to break helps me break theirs less often.

## Network Diagram

```txt
                          [WAN / NBN HFC]
                                 │
                            [UDM-Pro]
                                 │
         ┌───────────────────────┼───────────────────────┐
         │                       │                       │
    [Door Hub]              [USW-Flex]             [Aggregation]
         │                       │                       │
     [Camera]              ┌─────┴─────┐         ┌───────┼─────────┐
                           │           │         │       │         │
                      [Camera x2]  [U6-Pro]    [UNAS]  [UNAS]  [Pro-Max-24]
                                       │                           │
                                   ~~Mesh~~                    [Proxmox x4]
                                       │
                       ┌───────────────┴───────────────┐
                       │                               │
                  [U6-Mesh]                       [U6-Mesh]
                       │                               │
                [Lite-8-PoE]                   [Lite-8-PoE]
                       │                               │
                   [Study]                        [Living]
```

> **Note:** I might actually make a graphic for this instead of the ASCII art. 
This was way too much effort to build and I dread needing to add to it.

## Hardware

### Networking

| Device | Role |
|--------|------|
| UDM-Pro | Router, firewall, NVR |
| USW-Aggregation | 10Gbe backbone |
| USW-Pro-Max-24 | Main switch, Proxmox connectivity |
| USW-Flex | Camera switch |
| USW-Lite-8-PoE (x2) | Study and living room |
| U6-Pro | Primary AP |
| U6-Mesh (x2) | Mesh APs for study and living room |
| Unifi Door Hub Mini | Garage door control |

### Compute (Proxmox Cluster)

Four micro PCs form a Proxmox cluster with Ceph storage across 16TB of NVMe.

| Node | Hardware | CPU | RAM | Storage | NIC |
|------|----------|-----|-----|---------|-----|
| `px-melbourne` | Dell Optiplex 7010 Micro | i7-13700T (24t) | 64GB | 512GB + 4TB NVMe | 1Gbe + 2.5Gbe |
| `px-suzuka` | Dell Optiplex 7020 Micro | i7-14700T (28t) | 64GB | 512GB + 4TB NVMe | 1Gbe + 2.5Gbe |
| `px-bahrain` | Lenovo M70q Gen 6 | Ultra 7 265T (20t) | 64GB | 512GB + 4TB NVMe | 1Gbe + 2.5Gbe |
| `px-jeddah` | Lenovo M70q Gen 6 | Ultra 7 265T (20t) | 64GB | 512GB + 4TB NVMe | 1Gbe + 2.5Gbe |

Each node has the built-in 1Gbe NIC plus a 2.5Gbe NIC added in place of the 
WiFi card. Boot drives hold ISOs and CT templates; the 4TB drives form the 
Ceph pool. 

These sit in my server rack as 2x 1U mounts that hold 2 PCs each. I've been
contemplating selling the Optiplexes and getting another Lenovo M70q Gen 6 so
I'd have a 3-node cluster with identical hardware. This way I could build a
single power supply for all of them instead of dealing with the absolute mess
of power cables and bricks in the rack.

### Storage

| Device | Config | Capacity | Purpose |
|--------|--------|----------|---------|
| UNAS-Pro (x2) | 7x 10TB RAID 5 + hotspare | ~50TB each | Media, backups |
| Ceph (across nodes) | 4x 4TB NVMe | ~16TB raw | VM/CT storage |

### Power

2x CyberPower OR1000ERM1U (1000VA/600W) protecting the core infrastructure.

## Network

Internal services run on `*.home.lachlancox.dev`, resolved by the UDM-Pro's
internal DNS. Public services are exposed through a reverse proxy on my main
domain.

### VLANs

| VLAN | Name | Subnet | Purpose |
|------|------|--------|---------|
| 1 | Management | `192.168.1.0/24` | Infrastructure hardware |
| 2 | Internal Users | `10.10.20.0/24` | WiFi clients |
| 20 | Infrastructure Services | `192.168.30.0/24` | Proxmox, infra services |
| 21 | Internal Services | `192.168.32.0/24` | Internal-only services |
| 22 | Public Services | `192.168.33.0/24` | Internet-exposed services |

## Services

| Service | Type | VLAN | Description |
|---------|------|------|-------------|
| `infra-proxy` | LXC | 20 | Caddy reverse proxy |
| `infra-auth` | LXC | 20 | Authelia for SSO |
| `svc-plex` | LXC | 22 | Plex media server |
| `svc-headscale` | LXC | 22 | Self-hosted Tailscale control server |
| `svc-tandoor` | LXC | 21 | Recipe management |
| `svc-actual` | LXC | 21 | Actual Budget |
| `svc-media` | VM | 21 | The arr stack (Docker) |
| `svc-uptime` | LXC | 21 | Uptime Kuma |

> **Note:** Metrics and visibility are basically non-existent right now. 
Planning to add Grafana for dashboards at some point.

## Naming Convention

**Proxmox nodes** are named after F1 circuits: `px-{circuit}`. Specifically 
the name of the node is in order of the circuits ran in 2025. With the 
exception of the `px-shanghai` as I completely forgot that this happened when
I named the nodes and I don't want to go through the trouble of fixing the
naming now that they are setup.

**Services** follow a prefix convention:
- `infra-{name}` — Infrastructure services (VLAN 20)
- `svc-{name}` — Application services (VLAN 21/22)
- `wrk-{name}` — Work testing environments

**Notes:**
- Planning to adopt `svc-` for internal (VLAN 21) and `dmz-` for public (VLAN 22)
- Might add a second reverse proxy to separate internal and public traffic
