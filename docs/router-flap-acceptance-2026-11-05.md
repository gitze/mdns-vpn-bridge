# RutOS router production LAN interruption acceptance — 2026-11-05

## Result: PASS for sustained administrative LAN-down / automatic recovery

- User authorized the production test; no firmware/configuration migration or manual relay restart performed.
- Relay 0.4.0, LAN br-lan, peer and existing filters unchanged.
- Tools, ash syntax, interface flags and absence of a conflicting test service checked before execution.
- Temporary procd service `mdns-flap-acceptance` ran two independent instances: a worker and a fallback restore, each a separate process. No nohup/setsid dependency.
- Restore instance armed its marker before the worker touched the interface. Worker had an EXIT restoration trap; independent restore issued an additional UP after 50 seconds.

## Actual interruption and watchdog evidence

- A baseline relay process was running before the test.
- Worker held br-lan administratively down, reasserting DOWN once per second for 32 samples; every sample observed UP_BIT=0 (flags 0x1002).
- Measured interval from the first DOWN observation to worker restoration: 33.83 s.
- Relay log: `lan watchdog: interface br-lan is down (flags broadcast|multicast)` at both 15:33:16Z and 15:33:36Z.
- Actual procd instance.respawn events followed the watchdog exits, with starts at 15:33:21Z and 15:33:41Z.
- The relay process was automatically respawned twice by procd (three distinct PIDs in sequence); final running executable version 0.4.0.
- Final process started while LAN remained down, then resumed packet flow after UP without a manual restart. Backup restoration also executed shortly afterwards.

## Fresh end-to-end acceptance after recovery

- Real home LAN client queried raw mDNS, bypassing cached resolver results.
- Fresh authoritative response from home relay 192.168.10.10: webui.local A -> 192.168.20.11, received after 0.304 s.
- Fresh response from home relay: sensor.local A -> 192.168.20.12, received after 0.507 s.
- HTTP access: device A HTTP 302, device B (ESPHome) HTTP 200.
- Router packet counters increased in both directions. Final instance errors=2 during the DOWN phase, unchanged across two later status reads. These are cumulative transient write errors, not zero errors or ongoing failure.
- No manual relay, VPN or network-service restart used to recover.

## Cleanup and scope

- Both temporary test processes finished; temporary procd service stopped and its init file removed. Exact service-list readback returned an empty object; init file absence and br-lan UP verified.
- Test evidence retained at router `<router-test-dir>/` and local `<evidence-dir>/router-flap/` (scripts, worker/restore logs, baseline status, relay log, parsed fresh DNS responses).
- This closes the previously invalid target-router sustained-down/watchdog test. It does not prove behavior for sub-15-second flaps, physical switch carrier loss, changed LAN addresses or a missing WAN/VPN route.
- No relay code change required by this test. Linux manager-aware recovery and remaining acceptance items are separate work.
