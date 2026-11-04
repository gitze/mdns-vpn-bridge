# RutOS / OpenWrt packaging

Three packages, built with the Teltonika Puffin SDK (`puf apps build <name>`):

| Directory | Package | Contents |
|---|---|---|
| `../` (this repo's `packaging/rutos/` root) | `mdns-vpn-bridge` | binary, procd init, UCI config |
| `api/` | `mdns-vpn-bridge-api` | WebUI backend (`/usr/lib/lua/api/services/`), path map, ACL |
| `ui/` | `mdns-vpn-bridge-ui` | compiled Vue view (`/www/views/services/`), menu entry |

The UI package needs the API package (the ACL and the Lua service live there);
both need the main package.

## Build

```sh
scripts/build.sh                       # static armv7 binary
cp build/linux-armv7 packaging/rutos/files/bin/mdns-vpn-bridge

# inside the SDK (SDK apps/ + build.conf entries, see build.conf.snippet)
puf apps build mdns-vpn-bridge         # binary + init + UCI
puf apps build mdns-vpn-bridge-api     # Lua API
puf apps build mdns-vpn-bridge-ui      # Vue view (needs node/npm, ~1 min)
```

Resulting ipks land in `dist/packages/`.

## Install on the router

```sh
opkg install mdns-vpn-bridge_*.ipk mdns-vpn-bridge-api_*.ipk mdns-vpn-bridge-ui_*.ipk
uci set mdns-vpn-bridge.main.peer=100.x.y.z
uci commit mdns-vpn-bridge
/etc/init.d/mdns-vpn-bridge enable
/etc/init.d/mdns-vpn-bridge start
```

After that the entry **Services → mDNS VPN Bridge** appears in the WebUI: status
(interface, peer, filter, uptime, counters) and the configuration form
(enable, LAN interface, peer, port, both filters, service allowlist, stats
interval) with *Save and restart*.

No reboot and no WebUI restart are needed — the menu file is read on page load.

## Notes

- The WebUI framework is Teltonika's own (`vuci`); the plugin contract is
  documented in their SDK (`doc/app-types/Wrapper_frontend_app.md`). The build
  script and Vite config in `ui/` are independent implementations of that
  contract, not copies of SDK code.
- A firmware update wipes the overlay filesystem; reinstall the packages or
  build a firmware image with `puf build`.
- The UI reads the relay's JSON status from `http://127.0.0.1:53531/_status`
  (the `status_addr` UCI option / `-status` flag). If you change that address,
  update `STATUS_URL` in `api/files/usr/lib/lua/api/services/mdns_vpn_bridge.lua`.
