# Configuration file (`-config`)

The relay can be configured from a single JSON file instead of a long
`ExecStart` flag list. This is the recommended deployment for the Linux
systemd unit and for any supervisor that prefers a file over command
line arguments. The file is read once at startup.

```
mdns-vpn-bridge -config /etc/mdns-vpn-bridge/config.json
mdns-vpn-bridge check -config /etc/mdns-vpn-bridge/config.json   # validate only
```

RutOS keeps its own configuration path: the procd init script translates UCI
options into CLI flags, unchanged. The JSON file is a **Linux-side** deployment
adapter; it is not used by the RutOS package and the relay has no UCI
dependency.

## Schema

A JSON object. Every key is optional; an omitted key keeps its built-in
default. `lan_if` and `peer` must ultimately be supplied by file or flags. The keys map 1:1 to the CLI flags.

| Key | Type | CLI flag | Default | Meaning |
|---|---|---|---|---|
| `lan_if` | string | `-lan` | — (required) | LAN interface carrying mDNS (`ens18`, `br-lan`, …) |
| `lan_ip` | string | `-lanip` | from `lan_if` | override the LAN IPv4 |
| `peer` | string | `-peer` | — (required) | peer's overlay IPv4 (Tailscale/WireGuard address) |
| `peer_port` | number | `-peerport` | `53530` | UDP port of the unicast link, same on both sides |
| `link_bind` | string | `-linkbind` | `0.0.0.0` | local IPv4 the peer link socket binds to (hygiene aid, not authentication) |
| `lan2peer` | string | `-lan2peer` | `all` | `query` = forward DNS queries only, `all` = everything |
| `peer2lan` | string | `-peer2lan` | `all` | what is injected from the peer |
| `services` | array of strings | `-services` | `[]` (all) | case-insensitive DNS-label allowlist for LAN multicast → peer only |
| `dedupe` | duration string | `-dedupe` | `2s` | loop-suppression window; `0` selects the `2s` default, negative is rejected |
| `verbose` | boolean | `-v` | `false` | log every forwarded packet |
| `stats` | duration string | `-stats` | `300s` | stats log interval, `0` = off |
| `status_addr` | string | `-status` | `127.0.0.1:53531` | HTTP status endpoint (`off` disables it) |

Durations use Go's syntax: `300s`, `2s`, `1500ms`, `1m`.

The `services` array takes bare service types without the CLI comma joining,
e.g. `["_http", "_esphomelib", "_ipp"]`. An empty array (or an omitted key)
means "all services". The scope and bypass rules are identical to `-services`
(see the repository README, "The filter"): it filters only the LAN-multicast →
peer path and is not a security boundary.

There are **no secrets** in this file. The relay has no shared key or password;
confidentiality comes from the overlay and the firewall is the real boundary.
The file may therefore be world-readable (mode `0644`, owner `root:root`), which
is what the hardened unit (`DynamicUser=yes`, `ProtectSystem=strict`) needs to
read it.

## Migration and endpoint exposure

- Existing CLI-only units continue to work with the new binary. Do not replace a working unit with the file-based template until its JSON file is created and validated. Transfer existing flags to the matching keys; preserve site-specific systemd dependencies and overrides.
- The new unit requires a binary supporting `-config`/`check`; installed 0.3.0 binaries do not. Do not pair it with an old binary.
- Invalid configuration causes relay exit 2; the supplied unit uses `RestartPreventExitStatus=2` to avoid endless retries. Runtime failures still restart. There is no redundant ExecStartPre read.
- Status binds loopback by default. An explicit empty host, wildcard or non-loopback address can expose the unauthenticated status endpoint; retain loopback unless exposure is deliberate and firewalled.
- JSON keys are case-sensitive, unique and canonical. Null values are rejected, as are files larger than 1 MiB.

## Behaviour

- **Explicit file errors fail.** If `-config` is given and the file is missing,
  unreadable, not valid JSON, or fails the schema check, the relay prints the
  error to stderr and exits non-zero. It does **not** silently fall back to the
  built-in defaults. A typo in the path is an error, not an empty config.
- **Unknown keys are rejected.** A key that is not in the schema above is an
  error, so a misspelled `lan2peer`/`pear` cannot silently take a default.
- **CLI overrides the file.** Flags given on the command line take precedence
  over the corresponding file value. This lets a unit or an operator override a
  single setting without editing the file. A flag that is *not* given does not
  clobber the file value; the file value (or the built-in default) applies.
- **Omitted keys default.** A key that is absent from the file keeps the
  built-in default listed above. `lan_if` and `peer` have no default and must be
  present (in the file or as a flag).
- **Restart only — no hot reload.** The file is read once at startup. There is
  no `SIGHUP`, no inotify watch and no reload verb: after editing the config you
  must restart the service (`systemctl restart mdns-vpn-bridge`). The relay
  keeps forwarding the old configuration until then.
- **Pure validation.** `check -config` validates the file's syntax and schema
  only. It does **not** check that the interface exists, that the peer is
  reachable, or that any port is free — those are runtime conditions and are
  reported at startup. `check` exits `0` on a valid file and non-zero with a
  diagnostic on stderr otherwise, and never touches the network or opens a
  socket.

## Editing safely

Because the relay only reads the file at startup, an edit can be applied
atomically and validated before it ever affects a running instance:

1. Copy the current file to a candidate **in the same directory**, then edit
   only the candidate. The active file stays unchanged:
   ```sh
   sudo cp -p /etc/mdns-vpn-bridge/config.json /etc/mdns-vpn-bridge/config.json.new
   sudoedit /etc/mdns-vpn-bridge/config.json.new
   ```
2. Validate the candidate and publish it only on success:
   ```sh
   sudo mdns-vpn-bridge check -config /etc/mdns-vpn-bridge/config.json.new &&
     sudo chown root:root /etc/mdns-vpn-bridge/config.json.new &&
     sudo chmod 0644 /etc/mdns-vpn-bridge/config.json.new &&
     sudo mv /etc/mdns-vpn-bridge/config.json.new /etc/mdns-vpn-bridge/config.json &&
     sudo systemctl restart mdns-vpn-bridge
   ```
   The rename is on one filesystem: the next start reads either the complete
   old file or the complete new file. A validation failure does not overwrite
   the active file or restart the service.
3. Verify the new process:
   ```sh
   sudo systemctl status mdns-vpn-bridge
   mdns-vpn-bridge status
   ```

Offline validation cannot guarantee startup: an interface, address or port may
be unavailable. Keep a backup for rollback if the runtime start fails.

## Example

`packaging/debian/config.json` (shipped in the Debian tarball) is a starting
point with placeholders — replace `lan_if` and `peer` with your own values:

```json
{
  "lan_if": "ens18",
  "peer": "100.64.0.1",
  "lan2peer": "query",
  "peer2lan": "all",
  "services": ["_http", "_esphomelib"],
  "stats": "300s"
}
```

The `100.64.0.0/10` block is carrier-grade NAT space (what Tailscale hands out);
substitute your own overlay address. See the repository README, "The filter",
for why `lan2peer`/`peer2lan` must be asymmetric.
