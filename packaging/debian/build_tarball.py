#!/usr/bin/env python3
"""Package the static linux-amd64 relay binary for Debian/Ubuntu systems.

Produces a deterministic tarball (mtime/uid/gid pinned) containing the binary,
the hardened systemd unit and an install README. There is deliberately **no
auto-installer**: the README documents the manual steps, so nothing is copied
and no service is started until the operator does it. This is the Debian/amd64
deliverable alongside the RutOS IPK, not a raw binary drop.

Layout inside the tarball:

    mdns-vpn-bridge_<ver>_linux-amd64/
        mdns-vpn-bridge             static binary (0755)
        mdns-vpn-bridge.service     hardened systemd unit, file-based (0644)
        config.json                 sample JSON config with placeholders (0644)
        README                      manual install instructions
        LICENSE                     MIT

The systemd unit starts the relay with `-config /etc/mdns-vpn-bridge/config.json`
only; `config.json` is the sample the operator edits, validates with
`mdns-vpn-bridge check -config` and installs as root:root mode 0644 (the file
holds no secrets). See docs/configuration.md.
"""

import argparse
import gzip
import io
import pathlib
import tarfile

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent.parent  # repository root

# Sample JSON config shipped in the tarball; the operator edits the placeholders.
SAMPLE_CONFIG = HERE / "config.json"

DEFAULT_VERSION = "0.4.0"


def archive(members):
    """members: {name: (bytes, mode)} -> deterministic gzipped tar bytes."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.GNU_FORMAT) as tar:
        for name, (data, mode) in sorted(members.items()):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = mode
            info.mtime = 0
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            tar.addfile(info, io.BytesIO(data))
    # mtime=0 keeps the gzip header stable so the tarball hash is reproducible.
    return gzip.compress(buffer.getvalue(), mtime=0)


def build(binary, output, version):
    if not binary.is_file():
        raise SystemExit(f"relay binary not found: {binary}")

    top = f"mdns-vpn-bridge_{version}_linux-amd64"
    members = {
        f"{top}/mdns-vpn-bridge": (binary.read_bytes(), 0o755),
        f"{top}/mdns-vpn-bridge.service":
            ((ROOT / "packaging/systemd/mdns-vpn-bridge.service").read_bytes(), 0o644),
        f"{top}/config.json": (SAMPLE_CONFIG.read_bytes(), 0o644),
        f"{top}/README": ((HERE / "README.install").read_bytes(), 0o644),
        f"{top}/configuration.md": ((ROOT / "docs/configuration.md").read_bytes(), 0o644),
        f"{top}/LICENSE": ((ROOT / "LICENSE").read_bytes(), 0o644),
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(archive(members))
    print(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=pathlib.Path,
                        help="built relay binary (linux-amd64)")
    parser.add_argument("output", type=pathlib.Path,
                        help="output .tar.gz path")
    parser.add_argument("--version", default=DEFAULT_VERSION)
    args = parser.parse_args()
    build(args.binary.resolve(), args.output.resolve(), args.version)


if __name__ == "__main__":
    main()
