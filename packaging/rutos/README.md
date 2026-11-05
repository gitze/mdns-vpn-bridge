# RutOS / OpenWrt relay package

- Package: `mdns-vpn-bridge`.
- Contents: relay binary, procd init script and UCI defaults.
- Optional Vue UI/Lua adapter: separate project `mdns-vpn-bridge-rutos-ui`, not built from this directory.
- The relay works without the UI through its CLI, UCI, procd and local HTTP status endpoint.

## Build

From the relay repository:

```sh
scripts/build.sh
cp build/linux-armv7 packaging/rutos/files/bin/mdns-vpn-bridge
```

Inside the RutOS SDK, register the relay app using `build.conf.snippet`, then:

```sh
puf apps build mdns-vpn-bridge
```

## Install and configure

```sh
opkg install mdns-vpn-bridge_*.ipk
uci set mdns-vpn-bridge.main.peer=100.x.y.z
uci set mdns-vpn-bridge.main.lan_if=br-lan
uci commit mdns-vpn-bridge
/etc/init.d/mdns-vpn-bridge enable
/etc/init.d/mdns-vpn-bridge start
/usr/local/bin/mdns-vpn-bridge status
```

- The peer is the remote relay host's VPN-reachable IPv4 address.
- Optional UCI option `main.linkbind` sets the local IPv4 the peer link socket
  binds to (default `0.0.0.0` = every interface); it is a hygiene aid, not
  authentication.
- Do not run two relays in parallel: they conflict on local mDNS/link ports.
- RutOS IPKs install under `/usr/local`; existing `/usr/local/etc` symlinks map configuration and init scripts into `/etc`.
- Firmware upgrades may wipe overlay-installed packages; reinstall or include the relay in a firmware build.
- UI development, packaging and verification now belong to the separate UI project; see its README and `docs/packaging.md`.
