# Linux managed reconnect acceptance — 2026-11-05

## Result: PASS

- Production home-gw ens18 is managed by ifupdown with DHCP/dhclient; authorized reconnect used `ifdown ens18` followed by `ifup ens18`, not raw `ip link` commands.
- Independent systemd fallback timer armed before worker launch; worker executed in a separate transient service, independent of SSH. Fallback was cancelled after successful autonomous recovery and never triggered.
- DHCPRELEASE and old-client termination recorded. During 25 samples the interface UP bit was zero and no IPv4/default route was printed; first sample to managed ifup measured 25.07 s.
- DHCPDISCOVER/OFFER/REQUEST/ACK recorded; lease restored 192.168.10.10/24 and default via 192.168.10.1 automatically. No manual address/route repair.
- Relay watchdog detected ens18 DOWN; systemd automatically restarted the relay (restart counter incremented once, a new process). The new process retried the missing IPv4 four times, then started packet handling after DHCP recovery.
- Tailscale peer ping succeeded after reconnect. No manual relay or VPN restart.
- Fresh raw mDNS responses from home relay 192.168.10.10 confirmed webui.local -> 192.168.20.11 and sensor.local -> 192.168.20.12. HTTP device access 302/200.
- Post-test relay active, bidirectional counters increasing, errors=0. Transient worker exited status 0 / Result=success, fallback not triggered, test timers inactive/unloaded. No persistent unit/network configuration changes.

## Finding disposition

- The missing-default-route concern is closed for a normal ifupdown-managed down/up reconnect on this deployment. No host configuration or relay code fix is required by this test.
- Previous failure after raw administrative `ip link` manipulation remains a distinct observed behavior, not proof that normal manager-controlled recovery fails. No periodic route repair added.
- Physical carrier loss, DHCP server failure, changed leases and other host network managers are outside this acceptance scope.

## Evidence

- Local `<evidence-dir>/linux-reconnect/`: worker/restore scripts, worker.log, baseline.json, baseline-service.txt, relay-journal.log and parsed fresh DNS probe JSON files.
- Remote `<gateway-test-dir>/`: worker/restore scripts, worker log, baseline and worker.done; absence of fallback.triggered verified.
