#!/bin/sh
# Cross-builds mdns-vpn-bridge for common Linux targets into build/<name>.
set -e
cd "$(dirname "$0")/.."
GO=${GO:-go}
$GO version >/dev/null

LDFLAGS="-s -w"
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
