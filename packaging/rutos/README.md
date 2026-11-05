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

## Privileges

- The relay needs no privileges: the mDNS port (5353), the link port and the
  status port are all above 1024, and joining the multicast group
  (`IP_ADD_MEMBERSHIP`) requires no capability. Verified under the hardened
  systemd unit (Debian 13): interface enumeration, `IP_ADD_MEMBERSHIP` on
  `224.0.0.251:5353` and a loopback listener all succeed with an empty
  capability set.
- The procd service nevertheless runs as **root** by default: a RutOS package
  cannot rely on a dedicated unprivileged account being present, and procd's
  `user` option makes the service fail to start if the account is missing.
  This is a known, accepted limitation.
- Where a suitable account exists (OpenWrt base ships `nobody`/`nogroup`;
  confirm it on your firmware first), add before `procd_close_instance` in the
  init script to drop privileges:

  ```sh
  procd_set_param user nobody
  ```

## Security

- The peer link is plain UDP and the relay trusts the configured peer address
  only. Restrict the link port (default UDP 53530) to the peer's overlay address
  in the firewall — that rule, not the bind address, is the boundary.
- The init script rejects UCI values for `peer`, `lan_if` and `linkbind` that
  contain whitespace or start with `-`, and passes every value to procd as its
  own quoted argument, so a UCI value cannot smuggle an extra flag. The
  isolated test for this is `tests/init_test.sh` (`make check`).
