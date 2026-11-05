# RutOS upgrade lifecycle investigation — 2026-11-05

## Proven mechanism

- Installed Tailscale and relay packages use native `default_prerm` / `default_postinst` from `/lib/functions.sh`.
- Native helpers select init scripts from the package's registered `.list` using the configured installation-root prefix.
- Stop belongs to the old package's prerm; start belongs to the new package's postinst. Adding prerm only in the new package cannot retroactively stop a process started by an older package without that hook.
- This mechanism was reproduced with the actual router opkg 0.6.3/libsolv 0.7.28 and actual unmodified native helper file, in a separate chroot. The production opkg database, UCI configuration, relay and Tailscale services were not used by the fixture.

## Executed transaction tests

- Test package `mdns-lifecycle-fixture`, versions 0.2.0 and 0.3.0, uses a deliberately synthetic idempotent init script and small ARM Go daemon. These are NOT reconstructed original relay releases or a procd acceptance test.
- No proc/sys mounts, live ubus socket, network commands or vendor Tailscale hooks inside the chroot. `/sbin/perm` is a no-op fixture; service start/stop behavior is the only tested lifecycle concern.
- Original relay wrapper scripts around the native helpers are used for fixture hooks. No custom forced restart is added.
- A: old package without prerm -> new package with native hooks:
  - Before: PID 17833, executable version 0.2.0.
  - After: same PID 17833, running executable still 0.2.0, installed file 0.3.0.
  - Hook log: start -> later start -> already_running, with no intervening stop.
  - Reproduces the exact symptom class: new file with an old process.
- B: old package with native prerm -> new package with native hooks:
  - Before: PID 17926, executable version 0.2.0.
  - After: new PID 17971, running executable and installed file both 0.3.0.
  - Hook log includes stop followed by start.
- Both cases preserved edited configuration byte content; new default stored as the native `-opkg` file.
- Both fixture daemons explicitly stopped after assertions. Production package database contains no mdns-lifecycle-fixture installation; production relay still started at 13:02:37Z, version 0.4.0, cumulative errors unchanged at 24.

## Qualification

- Original 0.2.0 relay IPK was subsequently located and independently inspected: `/path/to/test-evidence/`, SHA256 `5d99e12cdff8e620d104d826458c221c42f16c7f95b96b9f56749fc5ccda50dd`. Its control archive contains only `.` and `./control`: no prerm, postinst, preinst, postrm or conffiles. Binary/init/config payload paths are correct.
- Historical missing-stop-hook defect is therefore established from the original artifact, and fixture A reproduces its process/file version mismatch. This is not a full replay of original procd behavior or a fresh production upgrade.
- Absence of old conffiles alone does NOT establish that an upgrade to a new package declaring conffiles loses configuration: both native transaction fixtures preserved the edited bytes. Do not infer a configuration-loss incident from the old control archive alone.
- Native helper behavior depends on PKG_UPGRADE. In this fixture transaction the log included disable/enable, so preservation of enable state must not be inferred merely from the upgrade branch in helper source. Automatic executable replacement is proven for fixture B; disabled-service policy and actual procd registration require separate assertions.
- Full production upgrade without an added manual restart remains distinct acceptance. No production upgrade was performed in this investigation.

## Evidence handles

- Local author-owned test inputs and execution output: `/path/to/test-evidence/` (`probe.go`, `build_fixtures.py`, `run_chroot.sh`, `chroot-run.log`).
- Router's isolated fixture roots: `/path/to/test-evidence/`; inputs `/path/to/test-evidence/`.
- Two failed harness setup roots removed after diagnosis; final successful run uses explicit `-f /etc/opkg.conf --dest root`, correct architecture configuration and separate writable databases. No offline-root option is used, so native hooks run inside the chroot rather than against the host.
