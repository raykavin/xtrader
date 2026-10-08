#!/usr/bin/env bash
# Tests of the switches of entrypoint.sh (MT5_READ_ONLY, MT5_ALLOW_REAL,
# MT5_ALGO_TRADING, MT5_MCP_FORWARD), without Docker, Wine or the terminal:
# its switch function alone, and the script itself with an invalid value, which
# it refuses before it starts anything. Run by make entrypoint-test.
set -euo pipefail

dir="$(cd "$(dirname "$0")" && pwd)"
entrypoint="$dir/entrypoint.sh"
failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

bash -n "$entrypoint" || fail "bash -n entrypoint.sh"

eval "$(sed -n '/^switch() {$/,/^}$/p' "$entrypoint")"
declare -F switch >/dev/null || {
	echo "FAIL: no switch function in entrypoint.sh" >&2
	exit 1
}

# expect NAME VALUE DEFAULT WANT, WANT "error" for a refusal; VALUE "-" unset
expect() {
	local name="$1" value="$2" default="$3" want="$4" got status=0
	if [[ "$value" == - ]]; then
		got="$(unset "$name"; switch "$name" "$default" 2>/dev/null)" || status=$?
	else
		got="$(export "$name=$value"; switch "$name" "$default" 2>/dev/null)" || status=$?
	fi
	if [[ "$want" == error ]]; then
		[[ "$status" != 0 && -z "$got" ]] || fail "$name=$value: $got (status $status), want a refusal"
	else
		[[ "$status" == 0 && "$got" == "$want" ]] || fail "$name=$value: $got (status $status), want $want"
	fi
}

for name in MT5_READ_ONLY MT5_ALLOW_REAL MT5_ALGO_TRADING MT5_MCP_FORWARD; do
	for value in 1 true TRUE True yes YES; do
		expect "$name" "$value" 0 1
		expect "$name" "$value" 1 1
	done
	for value in 0 false FALSE no No; do
		expect "$name" "$value" 0 0
		expect "$name" "$value" 1 0
	done
	expect "$name" - 0 0
	expect "$name" - 1 1
	expect "$name" "" 0 0
	expect "$name" "" 1 1
	for value in 2 on off y n enabled " 1" "1 " "true " tru -1 01; do
		expect "$name" "$value" 0 error
		expect "$name" "$value" 1 error
	done
done

# every switch read through switch, with its default
for want in 'allow_real="$(switch MT5_ALLOW_REAL 0)"' 'read_only="$(switch MT5_READ_ONLY 0)"' \
	'algo_trading="$(switch MT5_ALGO_TRADING 1)"' 'mcp_forward="$(switch MT5_MCP_FORWARD 1)"'; do
	grep -qxF "$want" "$entrypoint" || fail "entrypoint.sh without: $want"
done
if grep -nE '\$\{MT5_(READ_ONLY|ALLOW_REAL|ALGO_TRADING|MCP_FORWARD)' "$entrypoint"; then
	fail "entrypoint.sh reads a switch past switch()"
fi

# the script refuses an invalid value first: as root it would touch /data, so
# only as another user, with a PATH whose commands record any call
if [[ "$(id -u)" != 0 ]]; then
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	for command in Xvfb pgrep x11vnc wineboot wineserver wine winepath unzip socat gosu iconv mkdir; do
		printf '#!/bin/sh\necho %s >>"%s/calls"\n' "$command" "$tmp" >"$tmp/$command"
		chmod +x "$tmp/$command"
	done
	for name in MT5_READ_ONLY MT5_ALLOW_REAL MT5_ALGO_TRADING MT5_MCP_FORWARD; do
		status=0
		err="$(env -i PATH="$tmp:/usr/bin:/bin" HOME="$tmp" WINEPREFIX="$tmp/wine" "$name=maybe" \
			bash "$entrypoint" 2>&1 >/dev/null)" || status=$?
		[[ "$status" != 0 ]] || fail "$name=maybe: the entrypoint went on"
		[[ "$err" == "mt5: $name=maybe: not a switch, 1/true/yes or 0/false/no" ]] ||
			fail "$name=maybe: stderr $err"
		[[ ! -e "$tmp/calls" ]] || fail "$name=maybe: ran $(tr '\n' ' ' <"$tmp/calls")"
	done
else
	echo "skipped as root: the entrypoint with an invalid value" >&2
fi

if ((failures)); then
	echo "$failures failures" >&2
	exit 1
fi
echo "ok"
