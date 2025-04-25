#!/usr/bin/env bash
# Runs tools/mt5bridge with the Windows Python of the MetaTrader 5 terminal in
# Wine. The bridge starts the terminal when it isn't open, and again if it is
# closed, logged in the account with its saved password. Keep it running while
# the agents trade; Ctrl+C stops it.
#
#	tools/mt5bridge/run.sh                   # the Exness demo 198974329
#	MT5_LOGIN=87654321 MT5_SERVER=Exness-MT5Real11 tools/mt5bridge/run.sh --allow-real
#
# Extra arguments go to the bridge, eg: --read-only, --port 18813.
#
# The terminal the bridge opens is headless: drawn on a virtual X display,
# Xvfb $MT5_DISPLAY (:99 by default), started here when not running; it trades
# as on the desktop. MT5_HEADLESS=0 shows it on the desktop, eg: to log in by
# hand. Without Xvfb (apt install xvfb) it is shown, with a warning. A terminal
# open already is used as it is, where it is.
set -euo pipefail

login="${MT5_LOGIN:-198974329}"
server="${MT5_SERVER:-Exness-MT5Trial11}"
terminal="${MT5_TERMINAL:-C:\\Program Files\\MetaTrader 5 EXNESS\\terminal64.exe}"
python="${WINEPREFIX:-$HOME/.wine}/drive_c/Python312/python.exe"
export WINEDEBUG=-all

# The display comes before any program of Wine, winepath included: the first
# one starts the desktop of Wine, which keeps its display for good (its server
# stays up); started without one, eg: by the service at boot, before any login,
# every window fails ("no driver could be loaded") and the terminal exits.
if [[ "${MT5_HEADLESS:-1}" != 0 ]]; then
	display="${MT5_DISPLAY:-:99}"
	if ! command -v Xvfb >/dev/null; then
		echo "mt5bridge: no Xvfb (apt install xvfb): the terminal is shown on the desktop" >&2
	else
		if ! pgrep -f "^Xvfb $display( |$)" >/dev/null; then
			# the socket and the lock of an Xvfb killed, eg: with the service
			rm -f "/tmp/.X11-unix/X${display#:}" "/tmp/.X${display#:}-lock"
			# apart from this script: it outlives the bridge, for the next one
			setsid nohup Xvfb "$display" -screen 0 1366x768x24 -nolisten tcp >/dev/null 2>&1 </dev/null &
		fi
		for _ in $(seq 1 100); do
			[[ -S "/tmp/.X11-unix/X${display#:}" ]] && break
			sleep 0.1
		done
		if [[ ! -S "/tmp/.X11-unix/X${display#:}" ]]; then
			echo "mt5bridge: the Xvfb display $display isn't up after 10s" >&2
			exit 1
		fi
		export DISPLAY="$display"
		echo "mt5bridge: headless terminal, on the Xvfb display $display" >&2
	fi
fi
if [[ -z "${DISPLAY:-}" ]]; then
	echo "mt5bridge: no display for the terminal: install Xvfb, or set DISPLAY" >&2
	exit 1
fi

bridge="$(winepath -w "$(dirname "$(readlink -f "$0")")/mt5bridge.py" 2>/dev/null)"

if [[ ! -f "$python" ]]; then
	echo "mt5bridge: no Windows Python at $python, see tools/mt5bridge/mt5bridge.py" >&2
	exit 1
fi
if curl -s -m 2 http://127.0.0.1:18812/health >/dev/null 2>&1 && [[ " $* " != *" --port "* ]]; then
	echo "mt5bridge: a bridge already answers on 127.0.0.1:18812, stop it first" >&2
	exit 1
fi

exec wine "$python" -u "$bridge" --login "$login" --server "$server" --path "$terminal" "$@"
