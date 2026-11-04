# RutOS / OpenWrt packaging

Packages for Teltonika RutOS, built with the Puffin SDK (`puf apps build <name>`).

| Directory | Package | Contents | Status |
|---|---|---|---|
| `.` | `mdns-vpn-bridge` | binary, procd init, UCI config | works (verified on RUTX50) |
| `api/` | `mdns-vpn-bridge-api` | WebUI backend (Lua), path map, ACL | installs, but the route is not registered - see below |
| `ui/` | `mdns-vpn-bridge-ui` | WebUI page (Vue, compiled with Vite) | cannot be installed on stock firmware - see below |

The UI package needs the API package (the ACL and the Lua service live there);
both need the main package.

## Paths inside RutOS packages

RutOS installs ipk files with `dest root /usr/local`, and `/usr/local/etc`,
`/usr/local/lib` and `/usr/share` are symlinks into that tree. Consequences for
package file paths:

| Path in the package | Ends up at | Correct? |
|---|---|---|
| `bin/foo` | `/usr/local/bin/foo` | yes |
| `etc/init.d/foo` | `/etc/init.d/foo` (via `/usr/local/etc -> /etc`) | yes |
| `share/vuci/path.d/x.json` | `/usr/share/vuci/path.d/x.json` (via `/usr/share -> local/share`) | yes |
| `usr/share/vuci/path.d/x.json` | `/usr/local/usr/share/...` - a directory nobody reads | no |
| `www/views/...` | `/usr/local/www/...` - not the docroot | no |

The Lua service file is the exception: `usr/lib/lua/...` lands in
`/usr/local/usr/lib/lua`, which is in `package.path`, so both spellings work for
Lua modules.

## Why the WebUI package cannot work on the stock firmware

Measured on a RUTX50, RutOS 7.25.3:

- `/www` is a read-only squashfs (`touch /www/x` -> "Read-only file system").
  The documented plugin layout `/www/views/<app>/...` cannot be written at all.
- The UI is a compiled SPA: `/www/index.html.gz` plus 97 prebuilt chunks
  `/www/assets/app.<name>.app-<firmware-build>.js.gz`, and a single baked
  `/usr/share/vuci/menu.d/menu.json`. A view name from the menu is resolved
  against those build-tagged chunks.
- `menu_generate.lua` globs `/www/views` for plugin manifests, but that
  directory does not exist in the shipped image - a plugin can only be added
  when the image is built, not by `opkg install`.

So the UI app is only usable if it is baked into a firmware image together with
Teltonika's frontend build (not part of the SDK). It is kept here as a reference
implementation, not as a supported package.

The API package installs (Lua module at `/usr/local/lib/lua/api/services/`,
`path.d` entry at `/usr/share/vuci/path.d/`, ACL at `/usr/share/rpcd/acl.d/`),
yet `GET /api/mdns_vpn_bridge_f/status` answers 404 while the firmware's own
`/api/2fa_login` answers 401: the dispatcher (`/www/cgi-bin/api_dispatcher.lua`)
does not pick up third-party `path.d` entries even with correct paths and a
`uhttpd` restart. Also reference-only until that is understood.

What works without any UI: `mdns-vpn-bridge status`, UCI, and the procd service.

## Build

```sh
scripts/build.sh                       # static binaries, all architectures
cp build/linux-armv7 packaging/rutos/files/bin/mdns-vpn-bridge

# inside the SDK (apps/ + build.conf entries, see build.conf.snippet)
puf apps build mdns-vpn-bridge         # binary + init + UCI
puf apps build mdns-vpn-bridge-api     # Lua API
puf apps build mdns-vpn-bridge-ui      # Vue view (needs node/npm)
```

Resulting ipks land in `dist/packages/`.

## Install on the router

```sh
opkg install mdns-vpn-bridge_*.ipk
uci set mdns-vpn-bridge.main.peer=100.x.y.z
uci set mdns-vpn-bridge.main.lan_if=br-lan
uci commit mdns-vpn-bridge
/etc/init.d/mdns-vpn-bridge enable
/etc/init.d/mdns-vpn-bridge start
/usr/local/bin/mdns-vpn-bridge status
```

Router-side quirks worth knowing:

- `scp` is not available (dropbear without scp server); transfer files with
  `cat file | ssh root@router 'cat > /tmp/file'`.
- Two mDNS relays must not run in parallel: they conflict on ports 5353 and
  53530, so stop the old service before starting a replacement.
- A firmware update wipes the overlay filesystem; reinstall the packages or
  build a firmware image with `puf build`.
