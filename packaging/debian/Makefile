# mdns-vpn-bridge — Debian/amd64 packaging.
#
#   make check    # offline packaging test: builds a tarball with a MOCK binary
#                 # and asserts the sample config + systemd unit are inside.
#                 # No dpkg/apt install, no system files touched, no network.
check:
	python3 tests/packaging_test.py

.PHONY: check
