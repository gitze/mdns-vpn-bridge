#!/bin/sh
# init_test.sh — isolated test for the procd init script's argument handling.
#
# It sources files/etc/init.d/mdns-vpn-bridge with stubbed config_*/procd_*
# functions and inspects the exact argv handed to procd. This catches a UCI
# value that tries to smuggle an extra flag (e.g. peer="1.2.3.4 -v") without
# needing OpenWrt, a router or root.
#
# Usage: sh packaging/rutos/tests/init_test.sh
set -u

SCRIPT="${1:-$(dirname "$0")/../files/etc/init.d/mdns-vpn-bridge}"
[ -r "$SCRIPT" ] || { echo "cannot read $SCRIPT" >&2; exit 2; }

nl='
'
fail=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fail=1; }

# --------------------------------------------------------------- procd stubs
cmd_args=""
record() {
	if [ -z "$cmd_args" ]; then
		cmd_args="$1"
	else
		cmd_args="$cmd_args$nl$1"
	fi
}
config_load() { :; }
config_get() { eval "$1=\${CFG_$3:-$4}"; }
config_get_bool() { eval "$1=\${CFG_$3:-$4}"; }
procd_open_instance() { :; }
procd_close_instance() { :; }
procd_add_reload_trigger() { :; }
procd_set_param() {
	[ "${1:-}" = command ] || return 0
	shift
	for a in "$@"; do record "$a"; done
}
procd_append_param() {
	[ "${1:-}" = command ] || return 0
	shift
	for a in "$@"; do record "$a"; done
}

# shellcheck source=../files/etc/init.d/mdns-vpn-bridge
. "$SCRIPT"

reset() {
	cmd_args=""
	unset CFG_enabled CFG_lan_if CFG_peer CFG_peerport CFG_linkbind \
		CFG_lan2peer CFG_peer2lan CFG_services CFG_stats CFG_status_addr
}

base_argv() { # lan_if peer
	printf '%s' "/usr/local/bin/mdns-vpn-bridge${nl}-lan${nl}$1${nl}-peer${nl}$2${nl}-peerport${nl}53530${nl}-lan2peer${nl}all${nl}-peer2lan${nl}all${nl}-stats${nl}300s${nl}-status${nl}127.0.0.1:53531"
}

expect_argv() { # description expected_rc expected_argv
	if [ "$2" = "$rc" ] && [ "$3" = "$cmd_args" ]; then
		ok "$1"
	else
		bad "$1 (rc=$rc, want $2)"
		printf '%s' "$cmd_args" | sed 's/^/       got:  /'
	fi
}

expect_rejected() { # description
	if [ "$rc" != 0 ] && [ -z "$cmd_args" ]; then
		ok "$1"
	else
		bad "$1 (rc=$rc, expected rejection)"
		printf '%s' "$cmd_args" | sed 's/^/       got:  /'
	fi
}

# 1) normal configuration: each value is its own argument, no -linkbind present
reset
CFG_lan_if=br-lan
CFG_peer=100.64.0.1
start_service
rc=$?
expect_argv "normal argv is one argument per value" 0 "$(base_argv br-lan 100.64.0.1)"

# 2) optional linkbind becomes a separate argument when set
reset
CFG_lan_if=br-lan
CFG_peer=100.64.0.1
CFG_linkbind=100.88.0.2
start_service
rc=$?
expect_argv "linkbind appended as its own argument" 0 \
	"$(base_argv br-lan 100.64.0.1)${nl}-linkbind${nl}100.88.0.2"

# 3) a value with whitespace is not split into extra flags
reset
CFG_lan_if=br-lan
CFG_peer=100.64.0.1
CFG_services="_http, _ipp"
start_service
rc=$?
if [ "$rc" = 0 ] && printf '%s' "$cmd_args" | grep -qx -- "-services" \
	&& printf '%s' "$cmd_args" | grep -qx -- "_http, _ipp"; then
	ok "services value with a space stays a single argument"
else
	bad "services value with a space stays a single argument (rc=$rc)"
	printf '%s' "$cmd_args" | sed 's/^/       got:  /'
fi

# 4) flag injection is rejected before any instance is opened
for bad_value in "100.64.0.1 -v" "-v" "100.64.0.1 -lan2peer all"; do
	reset
	CFG_lan_if=br-lan
	CFG_peer="$bad_value"
	start_service
	rc=$?
	expect_rejected "peer='$bad_value' is rejected"
done

# 5) interface and linkbind are validated the same way
reset
CFG_lan_if="br lan"
CFG_peer=100.64.0.1
start_service
rc=$?
expect_rejected "lan_if with a space is rejected"

reset
CFG_lan_if=br-lan
CFG_peer=100.64.0.1
CFG_linkbind="100.88.0.2 -v"
start_service
rc=$?
expect_rejected "linkbind with a space is rejected"

reset
CFG_lan_if="-x"
CFG_peer=100.64.0.1
start_service
rc=$?
expect_rejected "lan_if with a leading dash is rejected"

# 6) a missing peer fails, a disabled service starts nothing
reset
CFG_lan_if=br-lan
start_service
rc=$?
expect_rejected "missing peer is rejected"

reset
CFG_enabled=0
start_service
rc=$?
expect_argv "disabled service opens no instance" 0 ""

if [ "$fail" = 0 ]; then
	echo "PASS"
	exit 0
fi
echo "FAILED"
exit 1
