#!/usr/bin/env python3
"""Wrap the relay IPK in the metadata archive the RutOS GUI upload expects.

A raw IPK upload fails ("zcat: no gzip magic" / "No .ipk files found"). The
bundle is a gzip tar containing:

    main                              case-sensitive metadata keys
    <pkg>_<ver>_<arch>.ipk            the package itself
    Packages.gz                       control stanza + Filename/Size/checksums

`main` needs the full firmware identifier (RUTX_R_00.07.25.3, not 00.07.25.3)
and tlt_name; RutOS installs by package name through a temporary file:// feed,
so Packages.gz must be present and carry the hashes.
"""

import argparse
import gzip
import hashlib
import io
import pathlib
import tarfile

from build_ipk import (
    archive,
    DEFAULT_APPNAME,
    DEFAULT_ARCH,
    DEFAULT_FIRMWARE,
    DEFAULT_ROUTER,
    DEFAULT_TLT_NAME,
    DEFAULT_VERSION,
)


def read_control(ipk_bytes):
    """Extract the control stanza from an ar-format IPK."""
    if ipk_bytes[:8] != b"!<arch>\n":
        raise ValueError("expected ar-format IPK")
    offset = 8
    while offset < len(ipk_bytes):
        header = ipk_bytes[offset:offset + 60]
        size = int(header[48:58])
        name = header[:16].decode().strip().rstrip("/")
        payload = ipk_bytes[offset + 60:offset + 60 + size]
        if name == "control.tar.gz":
            with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as tar:
                member = tar.extractfile("./control")
                if member is None:
                    raise ValueError("control file missing from control archive")
                return member.read().decode()
        offset += 60 + size + size % 2
    raise ValueError("missing control archive")


def build(ipk, output, version, arch, firmware, router, appname, tlt_name):
    data = ipk.read_bytes()
    control = read_control(data)

    metadata = (
        f"Package: {appname}\nVersion: {version}\nRouter: {router}\n"
        f"ipk_file: {ipk.name}\nArchitecture: {arch}\nFirmware: {firmware}\n"
        f"AppName: {appname}\ntlt_name: {tlt_name}\n"
        "Description: mDNS/Bonjour relay between two LANs over a unicast VPN link\n"
    )
    index = (
        control.rstrip() + f"\nFilename: {ipk.name}\nSize: {len(data)}\n"
        f"MD5Sum: {hashlib.md5(data).hexdigest()}\n"
        f"SHA256sum: {hashlib.sha256(data).hexdigest()}\n\n"
    )
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(archive({
        "main": (metadata.encode(), 0o644),
        ipk.name: (data, 0o644),
        "Packages.gz": (gzip.compress(index.encode(), mtime=0), 0o644),
    }))
    print(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("ipk", type=pathlib.Path)
    parser.add_argument("output", type=pathlib.Path)
    parser.add_argument("--version", default=DEFAULT_VERSION)
    parser.add_argument("--arch", default=DEFAULT_ARCH)
    parser.add_argument("--firmware", default=DEFAULT_FIRMWARE)
    parser.add_argument("--router", default=DEFAULT_ROUTER)
    parser.add_argument("--appname", default=DEFAULT_APPNAME)
    parser.add_argument("--tlt-name", default=DEFAULT_TLT_NAME)
    args = parser.parse_args()
    build(args.ipk.resolve(), args.output.resolve(), args.version, args.arch,
          args.firmware, args.router, args.appname, args.tlt_name)


if __name__ == "__main__":
    main()
