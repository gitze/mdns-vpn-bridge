#!/usr/bin/env python3
"""Offline packaging test for the Debian/amd64 tarball.

Builds the deliverable tarball with a **mock** relay binary and asserts the
sample JSON config and the hardened systemd unit are inside, with the expected
modes and content. It never installs a package, never touches a system path and
never opens a socket: everything happens in a throwaway temp directory.

Usage: python3 packaging/debian/tests/packaging_test.py
Exit:  0 = PASS, 1 = FAIL.
"""

import importlib.util
import io
import json
import pathlib
import re
import sys
import tarfile
import tempfile

HERE = pathlib.Path(__file__).resolve().parent          # .../packaging/debian/tests
DEBIAN = HERE.parent                                    # .../packaging/debian
ROOT = DEBIAN.parent.parent                             # repository root

# Keys the child0-chosen JSON schema must carry; the sample must ship all of
# them so an operator sees the full surface in one file.
SCHEMA_KEYS = {
    "lan_if", "lan_ip", "peer", "peer_port", "link_bind", "lan2peer",
    "peer2lan", "services", "dedupe", "verbose", "stats", "status_addr",
}

fail = 0


def ok(msg):
    print(f"ok   - {msg}")


def bad(msg):
    global fail
    fail = 1
    print(f"FAIL - {msg}")


def load_build_tarball():
    """Import build_tarball.py from the parent directory by path."""
    spec = importlib.util.spec_from_file_location(
        "build_tarball", DEBIAN / "build_tarball.py")
    assert spec and spec.loader, "cannot load build_tarball.py"
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def members_of(tar_bytes):
    """name -> (bytes, mode) for every regular member of a .tar.gz blob."""
    out = {}
    with tarfile.open(fileobj=io.BytesIO(tar_bytes), mode="r:gz") as tar:
        for info in tar.getmembers():
            f = tar.extractfile(info)
            data = f.read() if f is not None else b""
            out[info.name] = (data, info.mode)
    return out


def main():
    # --- 1. the sample config shipped in the tarball is valid and complete ----
    sample_path = DEBIAN / "config.json"
    if not sample_path.is_file():
        bad("sample config packaging/debian/config.json is missing")
        return 1
    try:
        sample = json.loads(sample_path.read_text())
    except json.JSONDecodeError as exc:
        bad(f"sample config is not valid JSON: {exc}")
        return 1
    ok("sample config parses as JSON")
    missing = SCHEMA_KEYS - set(sample)
    if missing:
        bad(f"sample config is missing schema keys: {sorted(missing)}")
    else:
        ok("sample config carries every schema key")
    extra = set(sample) - SCHEMA_KEYS
    if extra:
        bad(f"sample config has keys outside the schema: {sorted(extra)}")
    else:
        ok("sample config has no keys outside the schema")
    # Placeholders, not real addresses: the example must not look deployable.
    if "REPLACE_WITH" in str(sample.get("lan_if", "")) \
            and "REPLACE_WITH" in str(sample.get("peer", "")):
        ok("sample config uses placeholders for lan_if and peer")
    else:
        bad("sample config should use REPLACE_WITH placeholders for lan_if/peer")

    # --- 2. build the tarball with a MOCK binary ------------------------------
    mod = load_build_tarball()
    with tempfile.TemporaryDirectory() as tmp:
        tmp = pathlib.Path(tmp)
        mock = tmp / "linux-amd64"
        # A mock binary is enough: the builder copies bytes, it never runs them.
        mock.write_bytes(b"\x7fELF mock relay binary\n")
        out1 = tmp / "a.tar.gz"
        out2 = tmp / "b.tar.gz"
        mod.build(mock, out1, "9.9.9")
        mod.build(mock, out2, "9.9.9")

        if out1.read_bytes() == out2.read_bytes():
            ok("tarball is deterministic for identical input")
        else:
            bad("tarball is not byte-identical across two builds")

        top = "mdns-vpn-bridge_9.9.9_linux-amd64"
        members = members_of(out1.read_bytes())

        # --- 3. config + unit + binary + docs are all inside ------------------
        expected = {
            f"{top}/mdns-vpn-bridge": 0o755,
            f"{top}/mdns-vpn-bridge.service": 0o644,
            f"{top}/config.json": 0o644,
            f"{top}/configuration.md": 0o644,
            f"{top}/README": 0o644,
            f"{top}/LICENSE": 0o644,
        }
        for name, mode in expected.items():
            if name not in members:
                bad(f"archive is missing {name}")
                continue
            got_mode = members[name][1]
            if got_mode != mode:
                bad(f"{name} mode = {oct(got_mode)}, want {oct(mode)}")
            else:
                ok(f"archive contains {name} ({oct(mode)})")

        if set(members) != set(expected):
            bad(f"unexpected archive members: {sorted(set(members) - set(expected))}")

        # The shipped config.json must equal the repo sample byte for byte.
        if members.get(f"{top}/config.json", (b"", 0))[0] == sample_path.read_bytes():
            ok("archived config.json matches the repo sample")
        else:
            bad("archived config.json differs from the repo sample")

        # --- 4. the unit is file-based, hardened and secret-free --------------
        unit = members.get(f"{top}/mdns-vpn-bridge.service", (b"", 0))[0].decode()
        exec_lines = [ln.strip() for ln in unit.splitlines()
                      if ln.strip().startswith(("ExecStart=", "ExecStartPre="))]
        start = [ln for ln in exec_lines if ln.startswith("ExecStart=")]
        if start == ["ExecStart=/usr/local/bin/mdns-vpn-bridge -config /etc/mdns-vpn-bridge/config.json"]:
            ok("ExecStart takes only -config /etc/mdns-vpn-bridge/config.json")
        else:
            bad(f"unexpected ExecStart: {start}")
        if "RestartPreventExitStatus=2" in unit and not any(ln.startswith("ExecStartPre=") for ln in exec_lines):
            ok("configuration errors do not restart-loop; validation occurs in the relay")
        else:
            bad("invalid-config restart prevention missing or redundant precheck present")
        for want in ("DynamicUser=yes", "NoNewPrivileges=yes", "ProtectSystem=strict",
                     "ProtectHome=yes", "PrivateTmp=yes",
                     "RestrictAddressFamilies=AF_INET AF_UNIX AF_NETLINK",
                     "CapabilityBoundingSet="):
            if want in unit:
                ok(f"unit keeps hardening: {want}")
            else:
                bad(f"unit lost hardening line: {want}")
        # Look for secret *material* (a directive assigning a value), not the
        # word "secret" in prose such as "this file holds no secrets".
        secret_re = re.compile(
            r"(?im)^\s*(password|passwd|secret|token|psk|api[_-]?key|"
            r"private[_-]?key|environment|environmentfile)\s*=")
        hits = secret_re.findall(unit)
        if hits:
            bad(f"unit has a secret-carrying directive: {sorted(set(hits))}")
        else:
            ok("unit contains no secret material")

    if fail == 0:
        print("PASS")
        return 0
    print("FAILED")
    return 1


if __name__ == "__main__":
    sys.exit(main())
