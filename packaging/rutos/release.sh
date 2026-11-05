#!/bin/sh
# Build the mdns-vpn-bridge relay release artifacts.
#
#   VERSION=0.3.0 COMMIT=ac38fa0 sh packaging/rutos/release.sh
#
# Produces, under dist/ (git-ignored):
#   mdns-vpn-bridge_<ver>_<arch>.ipk                 RutOS IPK (opkg install)
#   mdns-vpn-bridge_<ver>_<router>_<fw>.tar.gz       RutOS GUI-uploadable bundle
#   mdns-vpn-bridge_<ver>_linux-amd64.tar.gz         Debian/amd64 tarball
#                                                    (binary + systemd unit +
#                                                    README; no auto-installer)
#   sha256sums.txt                                   checksums of all artifacts
# With ALLARCH=1 the cross-built binaries for every target are copied into dist/
# and included in sha256sums.txt as well.
#
# Provenance: the relay binaries embed VERSION and COMMIT. A release is cut from
# a committed base revision (COMMIT, e.g. ac38fa0) plus the release/packaging
# glue in the working tree, so `mdns-vpn-bridge help` prints e.g.
# "0.3.0 (ac38fa0)" — the base commit, not a commit of the uncommitted glue.
#
# Binaries are always rebuilt: stale binaries may not support the packaged
# config-file unit. SKIP_BUILD=1 is rejected.
#
# No installation, no router access, no source mutations. Safe to run repeatedly.
set -e
cd "$(dirname "$0")/../.."   # repository root

VERSION=${VERSION:-0.4.0}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "")}
ARCH=${ARCH:-cortexa7hf-neon-vfpv4}
FIRMWARE=${FIRMWARE:-RUTX_R_00.07.25.3}
FW_SHORT=${FIRMWARE#RUTX_R_}
ROUTER=${ROUTER:-RUTX}
GO=${GO:-go}
DIST=${DIST:-dist}
PYTHON=${PYTHON:-python3}

mkdir -p "$DIST"

# 1. Cross-build the relay binaries with an explicit release version.
if [ "${SKIP_BUILD:-0}" = "1" ]; then
	echo "SKIP_BUILD is unsupported: rebuild binaries to match configuration support and version" >&2
	exit 1
fi
VERSION="$VERSION" COMMIT="$COMMIT" GO="$GO" sh scripts/build.sh

# 2. linux-armv7 -> RutOS IPK.
IPK="$DIST/mdns-vpn-bridge_${VERSION}_${ARCH}.ipk"
"$PYTHON" packaging/rutos/build_ipk.py build/linux-armv7 "$IPK" \
	--version "$VERSION" --arch "$ARCH" --firmware "$FIRMWARE" --router "$ROUTER"

# 3. IPK -> GUI-uploadable bundle.
BUNDLE="$DIST/mdns-vpn-bridge_${VERSION}_${ROUTER}_${FW_SHORT}.tar.gz"
"$PYTHON" packaging/rutos/build_bundle.py "$IPK" "$BUNDLE" \
	--version "$VERSION" --arch "$ARCH" --firmware "$FIRMWARE" --router "$ROUTER"

# 4. linux-amd64 -> Debian tarball (binary + systemd unit + README, no installer).
DEB="$DIST/mdns-vpn-bridge_${VERSION}_linux-amd64.tar.gz"
"$PYTHON" packaging/debian/build_tarball.py build/linux-amd64 "$DEB" --version "$VERSION"

# 5. Checksums over every artifact. With ALLARCH=1 the cross-built binaries are
#    copied into dist/ first and checksummed too (they were previously omitted).
if [ "${ALLARCH:-0}" = "1" ]; then
	for b in build/linux-*; do
		cp "$b" "$DIST/"
	done
	( cd "$DIST" && sha256sum mdns-vpn-bridge_* linux-* > sha256sums.txt )
else
	( cd "$DIST" && sha256sum mdns-vpn-bridge_* > sha256sums.txt )
fi
echo "--- $DIST ---"
ls -l "$DIST"
cat "$DIST/sha256sums.txt"
