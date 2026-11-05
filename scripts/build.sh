#!/bin/sh
# Cross-builds mdns-vpn-bridge for common Linux targets into build/<name>.
set -e
cd "$(dirname "$0")/.."
GO=${GO:-go}
$GO version >/dev/null

# A release build pins VERSION explicitly (e.g. VERSION=0.5.0) so the embedded
# string is the release number, not a git-describe value that can read like a
# bare commit hash. COMMIT records the source revision for provenance.
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "")}
LDFLAGS="-s -w -X main.version=${VERSION}"
[ -n "$COMMIT" ] && LDFLAGS="$LDFLAGS -X main.commit=${COMMIT}"
echo "version=${VERSION} commit=${COMMIT:-<none>}"
mkdir -p build

build() { # name goarch [goarm] [gomips]
    name=$1; arch=$2; arm=$3; mips=$4
    out="build/$name"
    echo "building $out"
    GOOS=linux GOARCH="$arch" GOARM="$arm" GOMIPS="$mips" CGO_ENABLED=0 \
        $GO build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/mdns-vpn-bridge
}

build linux-amd64   amd64
build linux-arm64   arm64
build linux-armv7   arm   7
build linux-armv6   arm   6
build linux-mips    mips  "" hardfloat
build linux-mipsle  mipsle

ls -l build/
