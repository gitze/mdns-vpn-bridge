# Relay stabilization

- Scope: current working tree; original review: [FINDINGS.md](FINDINGS.md).
- State: implementation and integration verification completed; no release or production installation.
- Historical UI extraction/deletions must remain separate from stabilization commits.

## Findings disposition

| Finding | Result |
|---|---|
| 1 | Separate receive buffer for each reader; concurrent real UDP integration test. |
| 2 | Linux `IP_MULTICAST_ALL=0`; socket option test executed. `SO_BINDTODEVICE` not enabled: interface-scoped membership plus multicast isolation avoids adding privilege requirements; a two-interface namespace test remains unperformed. |
| 3 | Fatal reader errors return from `Run`; internal cancellation, socket closure and reader/statistics joins; EINTR/EAGAIN retried. LAN watchdog checks address, interface index and up/running flags every 15 seconds; explicit override membership is checked. |
| 4 | Optional `-linkbind` / UCI `linkbind`, default unchanged (all addresses); source-IP check and firewall requirement retained. Binding is not authentication; no custom crypto or interface-binding flag. |
| 5 | Bounded DNS-name parsing, label/case-aware matching, dotted entries, enumeration support. Compressed names and name-valued RDATA checked against encoded record boundaries. Packet-level filtering only; bundled unrelated records also pass, isolated address records cannot be associated across packets. Direction/bypass contract preserved and documented. |
| 6 | Minimum 12 bytes in both directions including unicast; own-source exclusion documented. No claim of full DNS validation with an empty allowlist. |
| 7 | Hard dedupe cap 8192, amortized FIFO expiry/eviction, timestamps assigned under lock, filtering before dedupe, additive top-level JSON `dedupe` counter. Existing directional JSON fields unchanged for UI compatibility. |
| 8 | Init command arguments individually quoted; isolated procd/UCI shell stubs exercise injection attempts and disabled behavior. |
| 9 | systemd DynamicUser/hardening; AF_NETLINK retained for Go interface discovery. RutOS root execution remains a documented limitation; no assumed user account. |
| 10 | Misleading override network-mask log removed. |
| 11 | Prefer non-link-local IPv4; ambiguity warning points to `-lanip`. |
| 12 | IPv4/MTU limitations documented, no arbitrary 1400-byte drops; synchronous status bind and runtime server-error propagation; positional CLI arguments rejected. CI has one vet/race/init-test job, least permissions, pinned Actions and release checksums. |

## Verification

- Host: dedicated Debian build host; Go 1.23.3, Linux amd64.
- Final isolated copy: `/path/to/test-evidence/` on build host.
- `gofmt -l internal cmd`: empty.
- `go vet ./...`: PASS.
- `go test -race -count=5 ./...`: PASS, both packages.
- `go test -race -run 'TestMcastSocket|TestRun' -v ./internal/relay`: PASS; multicast option test executed, not skipped.
- `go test ./internal/relay -run '^$' -fuzz FuzzServiceAllowed -fuzztime 10s -parallel 2`: PASS; final parent run 214096 executions. Fuzzer caps input at 4096 bytes; not an exhaustive proof.
- `make -C packaging/rutos check`: PASS.
- `busybox sh packaging/rutos/tests/init_test.sh`: PASS.
- Linux static builds: amd64, arm64, ARMv6, MIPS hardfloat, MIPS little-endian: PASS.
- `systemd-analyze verify`: PASS with ExecStart rewritten only in a scratch unit to point at the built test executable. Original unit verification initially failed because its production executable path does not exist on the build host; no production path was installed.
- Separate implementation-stage test exercised DynamicUser with empty capability set and AF_INET/AF_UNIX/AF_NETLINK on the build host; production systemd and RutOS operation remain untested for these changes.

## Second-round disposition and final acceptance

| Review item | Result |
|---|---|
| R1 | 15-second LAN watchdog; address/override loss, index change and interface-down tests. Three consecutive lookup failures are fatal; definite changes are fatal immediately. Short flaps between polls can be missed; see [isolated experiment](docs/network-flap-experiment.md), not a target-router guarantee. |
| R2 | Thread-safe per-direction 10-second error limiter; additive top-level `errors`; final pending summaries flushed after readers join, covered by a deterministic late-write regression. |
| R3 | Confirmed docs-only choice: no service-target cache; standalone host-name resolution with an allowlist is not guaranteed. |
| R4 | ASCII-only folding; Kelvin/non-UTF-8 regression tests. |
| R5 | Negative dedupe rejected by CLI and Run; zero retains 2-second default; text status shows dedupe/errors; UCI comment fixed; startup warnings use configured logger. |
| R6 | Only EADDRNOTAVAIL retried, bounded and Context-abortable; other bind failures immediately fatal. |
| R7 | Status positional arguments rejected; read transient backoff 2 ms, maximum 100 consecutive errors, success resets streak. |
| R8 | CI exposes skips; optional ARM-QEMU job; native and emulated results distinguished. |
| R9 | `.DS_Store` ignored; tests included in stabilization commit, historical UI removal separated. |

- Final integration copy: build-host `/path/to/test-evidence/`; Go 1.23.3.
- Parent executed `gofmt` check, `go build ./...`, `go vet ./...`, `go test -race -count=3 ./...`: PASS.
- Parent executed init-script tests under sh and BusyBox: PASS.
- Parent cross-builds: amd64, arm64, ARMv6, ARMv7, MIPS hardfloat, MIPS little-endian: PASS.
- Parent fuzz run `FuzzServiceAllowed -fuzztime 5s -parallel 2`: PASS, 107103 executions.
- Parent executed both final ARMv6 test binaries under privately extracted qemu-arm: PASS; multicast socket-option test SKIPPED (emulated socket-option support), not evidence for multicast isolation. Native execution of that test was verified separately in the initial round.
- Parent systemd unit syntax validation: PASS using scratch executable-path substitution, no production unit installed.
- Final reviews: standards/concurrency and spec axes; corrected shutdown-flush ordering, Run-level negative TTL validation, missing index-change coverage and overgeneralized flap claims. Original review text retained in FINDINGS.md.

## Remaining boundaries

- No live router or peer relay upgrade/restart; installed production versions unchanged.
- No end-to-end discovery/MTU/link-flap or two-interface isolation test of this new tree.
- UI 0.3.0 is a separate project/release; it does not expose optional `linkbind` and does not display the new top-level dedupe counter.
- User requested implementation with commits; include all new tests and keep historical UI extraction in a separate commit.
- Review original source findings together with this disposition; original document is preserved as evidence, not silently marked fully fixed.
