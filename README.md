# mdns-vpn-bridge

**mDNS/Bonjour across two LANs over a VPN tunnel — mDNS over VPN, Bonjour across subnets, without multicast routing.**

`mdns-vpn-bridge` relays mDNS discovery traffic between two sites connected by a
unicast-only overlay such as Tailscale, WireGuard or ZeroTier. Devices at site B
(printers, Chromecast, AirPlay, ESPHome, HomeKit, Sonos) become discoverable at
site A and vice versa — the records keep their real LAN addresses, so clients
connect through the existing subnet routes.

mDNS is link-local multicast (`224.0.0.251:5353`). VPN overlays and routed
networks do not carry it, so discovery stops at the subnet router. This tool
terminates mDNS on both sides and re-originates it locally over a unicast link.

## Why not avahi-reflector / mdns-repeater / umdns?

Those reflectors mirror mDNS between **interfaces of one host** — they need both
L2 segments to arrive on the same box (VLANs on a router). Across two sites
behind a VPN, nothing arrives locally; the far side is only reachable as
unicast. That is the gap this tool fills:

| Tool | Works across two VPN-connected sites |
|---|---|
| avahi-daemon `--enable-reflector` | no (one host) |
| `mdns-repeater`, `vfreex/mdns-reflector`, OpenWrt `umdns` | no (one host) |
| `Gandem/bonjour-reflector` | no (VLANs on one host) |
| `udpbroadcastrelay` (pfSense/OPNsense/OpenWrt) | no (host interfaces) |
| **mdns-vpn-bridge** | **yes — one instance per site** |

## Topology

```
   Site A (home)                          Site B (remote/garden)
┌─────────────────────┐              ┌──────────────────────┐
│ LAN 192.0.2.0/24   │              │ LAN 198.51.100.0/24   │
│   │        ▲        │              │   ▲         │        │
│   │ mDNS   │ mDNS   │   unicast    │   │ mDNS    │ mDNS   │
│   ▼        │        │   overlay    │   │         ▼        │
│  bridge A ─┴────────┼──────────────┼───┴──────── bridge B │
│  (subnet router)    │  Tailscale/  │  (subnet router)     │
│                     │  WireGuard   │                      │
└─────────────────────┘              └──────────────────────┘
```

Requirements:

- One Linux host per site with a LAN interface **and** a VPN interface
  (a subnet router is ideal; a plain host on the LAN also works for its own
  discovery traffic)
- Both hosts reach each other over the overlay (e.g. `100.x.y.z`)

## Install

Download a static binary from
[Releases](../../releases) (`linux-amd64`, `linux-arm64`, `linux-armv7`,
`linux-armv6`, `linux-mips`, `linux-mipsle`) or build it yourself:

```sh
go build ./cmd/mdns-vpn-bridge
```

### systemd (Debian/Ubuntu/…)

```ini
[Service]
ExecStart=/usr/local/bin/mdns-vpn-bridge -lan ens18 -peer 198.51.100.2 \
    -lan2peer query -peer2lan all -stats 300s
Restart=always
```

A ready-made unit with placeholders is in
[`packaging/systemd/`](packaging/systemd/mdns-vpn-bridge.service).

### Teltonika RutOS / OpenWrt

`packaging/rutos/` contains three Puffin-SDK apps that build `.ipk` packages:

| Package | What it adds |
|---|---|
| `mdns-vpn-bridge` | binary, procd init, UCI config |
| `mdns-vpn-bridge-api` | WebUI backend (Lua), path map, ACL — *reference only, see below* |
| `mdns-vpn-bridge-ui` | WebUI page: status + configuration form (Vue, compiled with Vite) — *reference only* |

```sh
# inside the RutOS SDK
cp -r /path/to/mdns-vpn-bridge/packaging/rutos   apps/mdns-vpn-bridge
cp -r /path/to/mdns-vpn-bridge/packaging/rutos/api apps/mdns-vpn-bridge-api
cp -r /path/to/mdns-vpn-bridge/packaging/rutos/ui  apps/mdns-vpn-bridge-ui
cp build/linux-armv7 apps/mdns-vpn-bridge/files/bin/mdns-vpn-bridge
cat apps/mdns-vpn-bridge/build.conf.snippet >> build.conf
puf apps build mdns-vpn-bridge mdns-vpn-bridge-api mdns-vpn-bridge-ui

opkg install mdns-vpn-bridge*.ipk
uci set mdns-vpn-bridge.main.peer=198.51.100.1
uci commit mdns-vpn-bridge
/etc/init.d/mdns-vpn-bridge enable && /etc/init.d/mdns-vpn-bridge start
```

The API and UI packages are a reference implementation, not a supported path:
on RutOS 7.25.3 `/www` is a read-only squashfs and the WebUI is a compiled SPA
whose view chunks and `menu.d/menu.json` are baked into the image, so a page
cannot be added by `opkg install`. Verified restrictions and the exact evidence
are in [`packaging/rutos/README.md`](packaging/rutos/README.md). Use
`mdns-vpn-bridge status`, UCI and the procd service instead.

## Verified deployment

Running on a Teltonika RUTX50 in a garden site, bridged to a Debian VM at home
over Tailscale (5G uplink):

- `webui.local` → `198.51.100.152`, `sensor.local` → the ESPHome node,
  `http://webui.local/` answers 302 — resolved from the other site's LAN
- counters after the migration: ~350 packets / 150 kB per 3 minutes in the
  garden→home direction, 28 packets / 3 kB home→garden (no filter on the
  garden side, which is the intended asymmetry)
- package installs via `opkg`, starts through procd (`S95`), survives reboot

## Configuration

| Flag | Default | Meaning |
|---|---|---|
| `-lan` | — | LAN interface carrying mDNS (`ens18`, `br-lan`, …) |
| `-lanip` | from `-lan` | override the LAN IPv4 |
| `-peer` | — | peer's overlay IPv4 (Tailscale/WireGuard address) |
| `-peerport` | `53530` | UDP port of the unicast link, same on both sides |
| `-lan2peer` | `all` | `query` = forward DNS queries only, `all` = everything |
| `-peer2lan` | `all` | what is injected from the peer |
| `-services` | all | comma-separated allowlist of service types for `-lan2peer` (substring match) |
| `-dedupe` | `2s` | loop-suppression window |
| `-stats` | `300s` | stats log interval, `0` = off |
| `-status` | `127.0.0.1:53531` | HTTP status endpoint (`off` disables it) |
| `-v` | off | log every forwarded packet |

### Status

Every instance serves its state as JSON on `http://127.0.0.1:53531/_status`;
the same values are available on the command line:

```console
$ mdns-vpn-bridge status
mdns-vpn-bridge v0.2.0
  lan        ens18 (192.0.2.5)
  peer       198.51.100.2:53530
  filter     lan2peer=query peer2lan=all
  services   -
  uptime     3d04h
  -> peer    10422 packets / 1.4 MB (dropped 118)
  -> lan     2214 packets / 302.1 kB (dropped 0)

$ mdns-vpn-bridge status --json | jq .to_peer
{ "packets": 10422, "bytes": 1402331, "dropped": 118 }
```

`mdns-vpn-bridge version` prints the build version.

### The filter — make it asymmetric

The filter exists to keep discovery chatter off the link, not for security.
It **must be asymmetric**:

- The **chatty side** (a normal office/home LAN with many devices announcing
  themselves) uses `-lan2peer query`: its queries reach the remote site, but
  its own announcements stay home. Remote devices can still be discovered,
  because they answer the queries that arrive.
- The **quiet side** uses `-lan2peer all`: if it also filtered, its own
  responses would be dropped and nothing would ever resolve.

Unicast packets addressed to the relay's own LAN address (legacy QU responses)
always pass, regardless of the filter.

Measured on a real setup (home ↔ garden over 5G): with `query` on the home
side, home → garden traffic is ~43 kB/min; without any filter ~107 kB/min
(~154 MB/day). Use `-services _http,_esphomelib,_ipp,_airplay` to narrow it
further.

## Security

- The unicast link is plain UDP. Confidentiality comes from the overlay
  (Tailscale/WireGuard are encrypted); the relay accepts packets only from the
  configured peer address, but it does not authenticate anything itself.
- Restrict the link port in your firewall to the peer's overlay address if the
  overlay is shared with untrusted nodes.
- mDNS responses from the far side appear on your LAN with the relay's source
  address while carrying the remote devices' real addresses in the records.
  That is normal for a relay, but be aware your LAN learns about the remote
  network.

## Limitations

- Records keep their real LAN addresses; the remote subnet must be routed to
  the client (subnet router, client route, or NAT).
- Link-local IPv6 addresses (`fe80::`) in relayed records are not usable on the
  other side.
- Discovery works; whether *pairing* protocols (AirPlay/HomeKit first-time
  pairing) work across a 50 ms WAN link depends on the protocol and is not
  guaranteed.
- A firmware update on an embedded router wipes the overlay filesystem —
  reinstall the package or bake it into the image (`puf build` for RutOS).

## Building

```sh
scripts/build.sh   # static binaries for amd64, arm64, armv7, armv6, mips, mipsle
go test ./...      # unit tests (no network needed)
```

No cgo, no dependencies — the binaries are fully static and run on musl-based
systems (RutOS, Alpine).

## License

[MIT](LICENSE)
