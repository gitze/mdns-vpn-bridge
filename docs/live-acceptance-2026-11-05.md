# 0.4.0 acceptance — 2026-11-05

## CURRENT STATUS (consolidated 2026-11-05)

> Prominent summary. Everything below is historical evidence of the 2026-11-05 acceptance run and is not rewritten. The earlier "Open / blocked" list is marked historical; this block is authoritative.

- **Closed / verified:** agreed core corrections (R3 and accepted limits below are documented, not implemented); two-interface isolation (lab); fresh discovery + device HTTP (live); real sustained router flap (production); managed Linux reconnect; real package upgrade 0.4.0 → 0.4.1 (RutOS GUI). Relay RUTX50 0.4.1, home-gw 0.4.0 (same core code).
- **Open (not upgrade-lifecycle):** current WebUI Save/Apply/Restart live acceptance — **blocked** (login declined today; no further attempts, no credentials requested). User reports the combined bundle **relay 0.4.1 + UI 0.3.1 installed successfully, but write/apply tests not yet done**. Reverse-direction large packets / full MTU: open. Production address change: lab passed, production open.
- **Accepted limits (deliberate, no implementation):** IPv4 only; service-target cache not implemented; RutOS/procd still root.
- No new feature implementation.

## Scope and authorization

- Production live tests, service/network interruption and reboots explicitly authorized by user.
- Both production processes run 0.4.0; Linux JSON configuration, RutOS UCI.
- No production configuration or firmware changes; temporary interface down/up and reboots executed. One lost DHCP default route restored from lease evidence (see finding).

## Live data plane — PASS

- Queries from actual home LAN client, not the relay's own IP.
- Fresh mDNS A responses for webui.local and sensor.local originate from the home relay and contain garden addresses.
- PTR/SRV/TXT discovery for _http and _esphomelib; dns-sd resolution/browsing exercised.
- HTTP device access: Venus 302 to GUI, ESPHome sensor 200.
- Independent captures on both LAN and VPN interfaces confirm relay path.
- Valid DNS payloads 1400/4000/8500 bytes traverse the live VPN and garden LAN; captures reassemble completely. Not an exhaustive path-MTU guarantee or a reverse-direction large-payload test.
- Own-source exclusion confirmed: relay-originated LAN queries excluded, client-originated queries forwarded.

## Isolation and address change — laboratory PASS

- Exact deployed amd64 binary hash matched the laboratory release artifact.
- Separate served-LAN, guest and peer netns/veth interfaces, with multicast membership on guest as positive control.
- IP_MULTICAST_ALL=1 reproduces cross-interface delivery; =0 prevents it.
- Release relay forwards served-LAN marker and zero guest markers, while guest listener receives its own marker.
- LAN address change detected, supervisor restarts with new address; persistent down/up recovered. Fixed 15-second watchdog interval unchanged.
- These are isolated build-host tests, not a production address change.

## Production reboots and interface interruptions

- home-gw reboot: boot ID <test-boot-id> -> <test-boot-id>. Relay auto-started from JSON.
- Router reboot: boot ID <test-boot-id> -> <test-boot-id>. Relay auto-started from UCI, running 0.4.0 confirmed.
- Router recorded 24 network-unreachable writes during boot before VPN route availability; later probes/counters show recovery without a manual relay restart. Error counter is cumulative and remained 24, not zero.
- home-gw ens18 down for 20 seconds: watchdog/supervisor restarted relay automatically (NRestarts=1); interface and relay recovered.
- Finding: raw ens18 down/up removed the DHCP-installed default route; VPN recovered after restoring gateway 192.0.2.1 from the lease. Follow-up inspection: ifupdown manages ens18 via DHCP and dhclient remained running. The test bypassed that manager with ip link set. This proves failure to restore a route after this administrative manipulation, not a defect during normal managed reconnect or physical carrier loss. Relay recovery passed; host network-recovery diagnosis remains qualified, not a confirmed product defect.
- CORRECTION after reading `/path/to/test-evidence/`: `ash: nohup: not found`. The router down/up shell did not execute at all. The earlier account of an executed bridge flap was incorrect; unchanged PID and working discovery are not evidence of flap recovery. Router production flap test is NOT EXECUTED.
- Final fresh webui.local and sensor.local responses again originated from the home relay; both HTTP services reachable; bidirectional counters increased. Linux errors=0, router errors remained 24.

## Open / blocked

> **HISTORICAL / SUPERSEDED.** Time-of-run state. Since then the managed Linux reconnect, the sustained router flap, and the automatic 0.4.0→0.4.1 upgrade activation have all PASSED (see linked reports and FINDINGS.md). Still open: WebUI Save/Apply/Restart live test, reverse large-payload / full-MTU, production address change. Authoritative summary: "CURRENT STATUS" above.

- WebUI Save/Apply/Restart against 0.4.0 not executed: saved logins rejected, new masked login declined. No further login attempts or credentials requested.
- Managed home-gw ifupdown/DHCP reconnect subsequently PASSED: DHCP restored address and default route automatically, relay/systemd and VPN recovered, fresh DNS/HTTP passed; see [managed reconnect acceptance](linux-managed-reconnect-2026-11-05.md). Prior raw ip-link behavior is not generalized to managed recovery.
- Router sustained-down/watchdog recovery: subsequently PASSED in the corrected production test with independent procd restoration, measured DOWN samples, automatic respawn and fresh discovery/HTTP after recovery; see [router flap acceptance](router-flap-acceptance-2026-11-05.md). Original nohup attempt remains invalid historical evidence.
- Full path-MTU proof, large-payload reverse direction, and production IP-address migration not claimed.
- RutOS package upgrade 0.4.0 -> 0.4.1 subsequently PASSED: native GUI upgrade log and procd stop/start, actual new running executable/hash, byte-preserved UCI and fresh DNS/HTTP verified; no agent-issued manual restart. See FINDINGS.md productive upgrade acceptance.

## Evidence

- Local evidence root: `/path/to/test-evidence/`.
- `lan-relay-live/EVIDENCE.json`, `lan-relay-live/REPORT.md`, captures/probe raw records.
- `artifacts/results.json` (`overall_ok=true`), interface isolation packet logs and watchdog logs.
- `post-reboot-webui.json`, `final-webui.json`, `final-solar-retry.json` contain fresh parsed replies.
- Report intentionally distinguishes live, isolated, blocked and qualified results; not a blanket all-tests-pass claim.

## Evidence review and issue classification

| Item | Classification | Decision / acceptance criterion |
| --- | --- | --- |
| Router bridge interruption | Invalid attempted test, not a demonstrated relay failure | Log says `ash: nohup: not found`; no bridge transition occurred. Repeat only with verified tools, independent restore and measured DOWN state. |
| Linux lost default route | Test-induced host state; normal network recovery not established | ens18 is ifupdown DHCP with running dhclient. Raw administrative down/up bypassed that manager. No relay route-management feature or periodic route workaround on this evidence. Test managed reconnect separately from carrier loss; capture route/lease state before and after. |
| Router boot write errors | Expected transient unavailability with successful recovery | 24 cumulative errors during unavailable VPN route, then stable counter and successful fresh responses. Not a crash or evidence that readiness gating is required. VPN readiness selection remains a separate feature proposal. |
| Local versus deployed binary hash | Explained: stale local 0.3.0 artifact, not non-reproducibility | Executed both artifacts' `help` on the build host: local SHA 51ce8773... identifies `0.3.0 (ac38fa0)`; deployed SHA 27268939... identifies `0.4.0 (6f96c9a)`. Child evidence incorrectly claimed both were 0.4.0. Deployed/tested release identity is consistent. Do not distribute ignored local build output as a current release. |
| WebUI Save/Apply/Restart | Blocked acceptance, not a demonstrated UI regression | Need valid authenticated session; no more stored-password guessing or retries after declined masked prompt. |
| Package upgrade automatic activation | Still open packaging lifecycle acceptance | Inspect actual installed hooks and native helpers; a successful reboot does not prove an upgrade restarted the running binary. A controlled upgrade must verify old/new running executable and preserved configuration without a manual restart masking the result. |
| Large payloads and two-interface isolation | Proven only within recorded scope | Actual 1400/4000/8500-byte forward-path reassembly and isolated Linux interface filtering passed. No blanket bidirectional PMTU or target-router isolation claim. |

- Read-only follow-up: Linux relay active, expected one watchdog restart, default route present, errors=0; router active, errors still 24 while traffic increases.
- No new product defect requiring a relay code change is established by this review. Outstanding test gaps and host-network diagnosis remain explicit; no VPN-selector implementation, production restart or new interruption performed during review.
