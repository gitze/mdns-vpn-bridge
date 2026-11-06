#!/usr/bin/env python3
"""Build the RutOS/OpenWrt IPK for the mdns-vpn-bridge relay.

Self-contained: no vendor SDK is required. The output is deterministic
(mtime/uid/gid pinned) so the IPK hash is reproducible from the same binary and
metadata.

Layout matches what the RutOS SDK / `dest root /usr/local` expects:

    bin/mdns-vpn-bridge        -> /usr/local/bin/mdns-vpn-bridge
    etc/init.d/mdns-vpn-bridge -> /etc/init.d/mdns-vpn-bridge  (/usr/local/etc -> /etc)
    etc/config/mdns-vpn-bridge -> /etc/config/mdns-vpn-bridge

The control archive carries:

    control   package metadata incl. Firmware/Router/AppName/tlt_name. RutOS
              indexes an installed package by its `tlt_name:` control field, so
              GUI offline-install completion needs it here, not only in the
              bundle `main`.
    conffiles /etc/config/mdns-vpn-bridge, so opkg preserves a user-edited
              configuration natively on future upgrades. This is the only
              preservation mechanism: there is no custom backup guard.
    postinst  runs the native default_postinst lifecycle, which per
    prerm     /lib/functions.sh starts (and, on a fresh install, enables) the
              package init script; prerm runs the native default_prerm. Neither
              script overwrites an existing UCI config or adds policy.
"""

import argparse
import gzip
import io
import pathlib
import tarfile

HERE = pathlib.Path(__file__).resolve().parent
FILES_DIR = HERE / "files"
CONTROL_DIR = HERE / "control"

DEFAULT_VERSION = "0.5.1"
DEFAULT_ARCH = "cortexa7hf-neon-vfpv4"
DEFAULT_FIRMWARE = "RUTX_R_00.07.25.3"
DEFAULT_ROUTER = "RUTX"
DEFAULT_APPNAME = "mdns-vpn-bridge"
DEFAULT_TLT_NAME = "mDNS VPN Bridge"
DEFAULT_MAINTAINER = "mdns-vpn-bridge contributors"

# Data payload: (source path relative to files/, mode). The binary is the only
# member that must already exist; the config/init come from the repo.
DATA_MEMBERS = (
    ("bin/mdns-vpn-bridge", 0o755),
    ("etc/init.d/mdns-vpn-bridge", 0o755),
    ("etc/config/mdns-vpn-bridge", 0o644),
)
# Control payload: (file in control/, mode). conffiles lists one path per line.
# No preinst/postrm: configuration is preserved solely by the conffiles
# declaration (proven with the router's opkg), so there is no custom guard and
# nothing is written under /tmp.
CONTROL_MEMBERS = (
    ("conffiles", 0o644),
    ("postinst", 0o755),
    ("prerm", 0o755),
)


def archive(members):
    """members: {name: (bytes, mode)} -> deterministic gzipped tar bytes."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.GNU_FORMAT) as tar:
        for name, (data, mode) in sorted(members.items()):
            info = tarfile.TarInfo("./" + name)
            info.size = len(data)
            info.mode = mode
            info.mtime = 0
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            tar.addfile(info, io.BytesIO(data))
    # mtime=0 keeps the gzip header stable so the IPK hash is reproducible.
    return gzip.compress(buffer.getvalue(), mtime=0)


def build(binary, output, version, arch, firmware, router, appname, tlt_name, maintainer):
    if not binary.is_file():
        raise SystemExit(f"relay binary not found: {binary}")

    data_members = {}
    for rel, mode in DATA_MEMBERS:
        path = binary if rel == "bin/mdns-vpn-bridge" else FILES_DIR / rel
        if not path.is_file():
            raise SystemExit(f"package file not found: {path}")
        data_members[rel] = (path.read_bytes(), mode)

    control_members = {}
    for rel, mode in CONTROL_MEMBERS:
        path = CONTROL_DIR / rel
        if not path.is_file():
            raise SystemExit(f"control file not found: {path}")
        control_members[rel] = (path.read_bytes(), mode)

    installed_size = sum(len(data) for data, _ in data_members.values())
    control = (
        f"Package: {appname}\n"
        f"Version: {version}\n"
        f"Architecture: {arch}\n"
        f"Firmware: {firmware}\n"
        f"Router: {router}\n"
        f"AppName: {appname}\n"
        f"tlt_name: {tlt_name}\n"
        f"Installed-Size: {installed_size}\n"
        f"Maintainer: {maintainer}\n"
        "Section: net\n"
        "Description: mDNS/Bonjour relay between two LANs over a unicast VPN link\n"
    )
    control_members["control"] = (control.encode(), 0o644)

    output.parent.mkdir(parents=True, exist_ok=True)
    parts = {
        "debian-binary": b"2.0\n",
        "control.tar.gz": archive(control_members),
        "data.tar.gz": archive(data_members),
    }
    with output.open("wb") as out:
        out.write(b"!<arch>\n")
        for name, data in parts.items():
            header = f"{name.ljust(16)}{0:<12}{0:<6}{0:<6}{'100644':<8}{len(data):<10}`\n"
            out.write(header.encode("ascii"))
            out.write(data)
            if len(data) % 2:
                out.write(b"\n")
    print(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=pathlib.Path, help="built relay binary (linux-armv7)")
    parser.add_argument("output", type=pathlib.Path, help="output .ipk path")
    parser.add_argument("--version", default=DEFAULT_VERSION)
    parser.add_argument("--arch", default=DEFAULT_ARCH)
    parser.add_argument("--firmware", default=DEFAULT_FIRMWARE)
    parser.add_argument("--router", default=DEFAULT_ROUTER)
    parser.add_argument("--appname", default=DEFAULT_APPNAME)
    parser.add_argument("--tlt-name", default=DEFAULT_TLT_NAME)
    parser.add_argument("--maintainer", default=DEFAULT_MAINTAINER)
    args = parser.parse_args()
    build(args.binary.resolve(), args.output.resolve(), args.version, args.arch,
          args.firmware, args.router, args.appname, args.tlt_name, args.maintainer)


if __name__ == "__main__":
    main()
