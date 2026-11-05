# Isolated IPv4 interface-flap experiment

- Scope: Linux 6.12.113, iproute2 6.15.0, Go 1.23.3; dedicated network namespace and fresh veth pair on the build host.
- Production LAN/VPN interfaces were not changed; test namespace removed afterwards.
- Throwaway Go probe mirrored relay socket options: `IP_MULTICAST_ALL=0`, interface-scoped `IP_ADD_MEMBERSHIP`, `IP_MULTICAST_IF`, multicast TTL 255; multicast and LAN-unicast sockets remained open across transitions.
- This was a socket-behavior experiment, not an end-to-end test of production discovery or a complete relay deployment.

## Observations

| Transition | Observed result |
|---|---|
| Interface down for 0.3 s, then up with unchanged address | Group membership remained visible; multicast and unicast packet flow resumed with existing sockets. |
| Remove old IPv4 and add a different IPv4 | Multicast writes returned `network is unreachable`; old-address unicast socket received no packets; sockets remained open without a read error. |
| Persistent interface down | Sends failed with `network is unreachable`; reception stopped; sockets remained open. |

## Decision and limits

- Add 15-second polling of interface index, up/running flags and selected IPv4; with `-lanip`, verify that the override remains assigned.
- Exit on a definitive mismatch so the supervisor can restart; tolerate only a bounded number of lookup errors.
- A down/up cycle entirely between checks can be missed.
- The unchanged-address short flap recovered in this experiment only. This does not prove recovery on every kernel, driver, bridge or RutOS/OpenWrt target.
- Address-change/down failures are evidence for a watchdog; no claim that polling detects every possible network failure.
- Separate two-interface receive-isolation, MTU and production discovery tests remain open.

## Evidence provenance

- Implementation-stage probe and output: build-host scratch `<build-host-scratch-dir>/`.
- A detailed implementation-stage record was kept locally during the experiment.
- The throwaway probe is not part of this repository; these observations are a bounded experiment record, not a reproducible automated CI fixture.
