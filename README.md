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
[`packaging/systemd/`](packaging/systemd/mdns-vpn-bridge.service). It runs the
relay as a transient unprivileged user (`DynamicUser=yes`) with a minimal
sandbox — the mDNS and status ports are both above 1024, so no privileges are
needed.

### Teltonika RutOS / OpenWrt

[`packaging/rutos/`](packaging/rutos/README.md) builds the relay package:
`mdns-vpn-bridge` contains the binary, procd init script and UCI defaults.

The optional **mdns-vpn-bridge-rutos-ui** is a separate project, with its own
Vue UI, Lua adapter, build and tests. It depends on the relay package; it is
not part of this source tree. The relay does not require the UI.

```sh
# inside the RutOS SDK, build the relay itself
cp -r /path/to/mdns-vpn-bridge/packaging/rutos apps/mdns-vpn-bridge
cp build/linux-armv7 apps/mdns-vpn-bridge/files/bin/mdns-vpn-bridge
# configure the main app as described in packaging/rutos/build.conf.snippet
puf apps build mdns-vpn-bridge

# on the router, install the main package (or use the existing installation)
opkg install mdns-vpn-bridge_*.ipk
uci set mdns-vpn-bridge.main.peer=198.51.100.1
uci commit mdns-vpn-bridge
/etc/init.d/mdns-vpn-bridge enable && /etc/init.d/mdns-vpn-bridge start
```

Build the optional UI in its own checkout:
`cd /path/to/mdns-vpn-bridge-rutos-ui && make`.
The relay itself is administered using `mdns-vpn-bridge status`, UCI and procd.

## Verified deployment

Running on a Teltonika RUTX50 in a garden site, bridged to a Debian VM at home
over Tailscale (5G uplink):

- `webui.local` → `198.51.100.152`, `sensor.local` → the ESPHome node,
  `http://webui.local/` answers 302 — resolved from the other site's LAN
- counters after the migration: ~350 packets / 150 kB per 3 minutes in the
  garden→home direction, 28 packets / 3 kB home→garden (no filter on the
  garden side, which is the intended asymmetry)
- package installs via `opkg`, starts through procd (`S95`), survives reboot
- the same version runs as `mdns-vpn-bridge.service` (systemd) on the Debian
  side; both instances report `mdns-vpn-bridge status` with `v0.2.0`

## Stabilization status

- Current uncommitted corrections and verification: [STABILIZATION.md](STABILIZATION.md).
- These relay changes are not yet released or installed; the separate RutOS UI 0.3.0 release does not upgrade the relay binary.

## Configuration

| Flag | Default | Meaning |
|---|---|---|
| `-lan` | — | LAN interface carrying mDNS (`ens18`, `br-lan`, …) |
| `-lanip` | from `-lan` | override the LAN IPv4 |
| `-peer` | — | peer's overlay IPv4 (Tailscale/WireGuard address) |
| `-peerport` | `53530` | UDP port of the unicast link, same on both sides |
| `-linkbind` | `0.0.0.0` | local IPv4 the peer link socket binds to (e.g. this host's overlay address); a hygiene aid, not authentication |
| `-lan2peer` | `all` | `query` = forward DNS queries only, `all` = everything |
| `-peer2lan` | `all` | what is injected from the peer |
| `-services` | all | comma-separated, case-insensitive DNS-label allowlist for LAN multicast → peer only (e.g. `_http` or `_http._tcp`); other paths bypass it (see below) |
| `-dedupe` | `2s` | loop-suppression window; `0` selects the `2s` default, a negative value is rejected |
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
  dedupe     57 suppressed
  errors     0 write errors

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

> **Scope of `-services` (important).** The service allowlist applies **only**
> to the LAN-multicast → peer path. Everything else bypasses it:
>
> - unicast replies addressed to the relay's own LAN address (legacy QU
>   responses) — these are never filtered;
> - all peer → LAN traffic, which `-peer2lan` controls instead;
> - packets shorter than a DNS header, which are dropped regardless.
>
> It is a chatter filter, **not a security boundary**, and it never applies to
> the peer → LAN direction.

With a service allowlist, malformed DNS packets are dropped. Matching occurs on
DNS names, not TXT text or byte substrings; comparison is label-exact and
case-insensitive (ASCII), so `_http` matches `_http._tcp.local` but never
`_https._tcp.local`. Service enumeration (`_services._dns-sd._udp`) is always
allowed while an allowlist is set. The filter permits entire packets, including
bundled records for other services; it is not a per-record security boundary.

**Host-name-only resolution is not guaranteed while an allowlist is set.**
A standalone A/AAAA packet for a bare name (`ping drucker.local`) carries no
service name, so it is dropped on the filtered path. There is deliberately **no
name cache**: the relay does not remember earlier service records to re-admit
such packets. Either add the relevant service type to `-services`, or leave the
allowlist empty if general `.local` resolution must work across the link.

Measured on a real setup (home ↔ garden over 5G): with `query` on the home
side, home → garden traffic is ~43 kB/min; without any filter ~107 kB/min
(~154 MB/day). Use `-services _http,_esphomelib,_ipp,_airplay` to narrow it
further.

### Relay behaviour

- **Own traffic is not relayed.** mDNS that the relay host itself originates on
  the LAN (for example a local avahi) arrives with the relay's own LAN address
  as source and is dropped, so it is never echoed to the peer. Traffic from
  every other host on the LAN is relayed normally.
- **Dedupe window.** Byte-identical packets seen within `-dedupe` (default
  `2s`) are dropped, in both directions. This is what breaks the loop between
  the two sites; the trade-off is that a legitimate retransmission of an
  identical packet inside that window (a repeated query, say) is also dropped.
  Lower the window if that matters on your network.
- **Packet size.** Packets up to 9000 bytes are relayed unchanged; there is no
  MTU limit and no size-based dropping. A tunnel with a smaller MTU (WireGuard
  defaults to ~1420) may fragment or drop large mDNS packets — that is left to
  the overlay.
- **IPv4 only.** Only IPv4 mDNS (`224.0.0.251`) is relayed. IPv6 mDNS
  (`ff02::fb`) is not touched.
- **LAN watchdog.** Every 15 s the relay re-checks the LAN interface's index,
  flags (`up`/`running`) and address (or, with `-lanip`, whether the override
  address is still on the interface). Open UDP sockets do not report an error
  when the interface loses its address or is recreated, so without this check
  the process would keep running as a half-dead instance the supervisor never
  restarts. On a change the relay exits with an error and the supervisor
  (systemd/procd) restarts it, rebinding to the new address.
- **Short link flaps.** A down/up cycle of the LAN interface that is shorter
  than the 15 s check interval is not detected. On the tested kernel (Linux
  6.12, isolated network namespace) such a cycle with an unchanged IP kept the
  IPv4 multicast membership and address, and packet flow continued in both
  directions (docs/network-flap-experiment.md); this is an observation on the
  tested kernel, not a general guarantee. Only a persistent change (address
  lost/changed, interface recreated or left down) is reported.

## Security

- The unicast link is plain UDP. Confidentiality comes from the overlay
  (Tailscale/WireGuard are encrypted); the relay accepts packets only from the
  configured peer address, but it does not authenticate anything itself.
- `-linkbind` narrows which local address the link socket is bound to. It is a
  routing/hygiene aid, **not** authentication: the peer check is still a plain
  source-address comparison and can be spoofed by anyone who can reach the
  port. Binding does not remove the need for a firewall rule.
- The firewall is the real boundary: restrict the link port (default UDP 53530)
  to the peer's overlay address so no other overlay node can inject mDNS into
  your LAN.
- mDNS responses from the far side appear on your LAN with the relay's source
  address while carrying the remote devices' real addresses in the records.
  That is normal for a relay, but be aware your LAN learns about the remote
  network.

## Limitations

- Records keep their real LAN addresses; the remote subnet must be routed to
  the client (subnet router, client route, or NAT).
- IPv6 mDNS (`ff02::fb`) is not relayed; only IPv4 (`224.0.0.251`).
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
