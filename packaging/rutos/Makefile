# mdns-vpn-bridge — Puffin/RutOS SDK app (no compilation step inside the SDK)
#
# The static Go binary must be built beforehand from the repository root:
#   scripts/build.sh          (produces build/linux-armv7)
#   cp build/linux-armv7 files/bin/mdns-vpn-bridge
#
# Target paths inside the package (RutOS installs with "dest root /usr/local"):
#   bin/mdns-vpn-bridge            -> /usr/local/bin/mdns-vpn-bridge
#   etc/init.d/mdns-vpn-bridge     -> /etc/init.d/mdns-vpn-bridge  (/usr/local/etc -> /etc)
#   etc/config/mdns-vpn-bridge     -> /etc/config/mdns-vpn-bridge
DESTDIR ?= /
FILES_DIR := files
BINARY := $(FILES_DIR)/bin/mdns-vpn-bridge

all:
	@test -x $(BINARY) || { echo "ERROR: $(BINARY) missing — build it with scripts/build.sh first"; exit 1; }

install: all
	install -d $(DESTDIR)/bin $(DESTDIR)/etc/init.d $(DESTDIR)/etc/config
	install -m 755 $(BINARY) $(DESTDIR)/bin/mdns-vpn-bridge
	install -m 755 $(FILES_DIR)/etc/init.d/mdns-vpn-bridge $(DESTDIR)/etc/init.d/mdns-vpn-bridge
	install -m 644 $(FILES_DIR)/etc/config/mdns-vpn-bridge $(DESTDIR)/etc/config/mdns-vpn-bridge

clean:
	@:  # the binary belongs to the repository build, not to the SDK

# Isolated test of the init script's argument handling (no OpenWrt needed).
check:
	sh tests/init_test.sh

# Release packaging (self-contained, no SDK needed): IPK + GUI bundle in DIST.
VERSION ?= 0.5.1
COMMIT ?=
DIST ?= ../../dist
release:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DIST=$(abspath $(DIST)) sh release.sh

.PHONY: all install clean check release
