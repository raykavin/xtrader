#!/usr/bin/env bash
# The start of the container of MT5. As root: the volume /data given to the
# user of Wine, which refuses a prefix of another owner (eg: copied from
# another machine), then the rest as it. As the user: the virtual display, the
# Wine prefix with the Windows Python of the bridge and the terminal of Exness
# (installed on the first start), Algo Trading on, the VNC and the forward of
# the MCP server of the terminal when asked, then tools/mt5bridge/run.sh, the
# bridge listening on the network of the compose with its token.
#
# The environment (services/mt5/.env): MT5_LOGIN, MT5_SERVER, MT5_PASSWORD
# (the terminal logs in with it), MT5_BRIDGE_TOKEN, MT5_ALLOW_REAL,
# MT5_READ_ONLY, MT5_VNC_PASSWORD, MT5_ALGO_TRADING, MT5_MCP_FORWARD.
set -euo pipefail

user=trader
if [[ "$(id -u)" == 0 ]]; then
	mkdir -p /data
	uid="$(id -u "$user")"
	[[ "$(stat -c %u /data)" == "$uid" ]] || chown "$user:$user" /data
	if [[ -d /data/wine && "$(stat -c %u /data/wine)" != "$uid" ]]; then
		echo "mt5: /data/wine given to $user" >&2
		chown -R "$user:$user" /data/wine
	fi
	exec gosu "$user" "$0" "$@"
fi

log() { echo "mt5: $*" >&2; }

downloads=/opt/xtrader/downloads
python_dir="$WINEPREFIX/drive_c/Python312"
terminal_dir="$WINEPREFIX/drive_c/Program Files/MetaTrader 5 EXNESS"
export MT5_TERMINAL="${MT5_TERMINAL:-C:\\Program Files\\MetaTrader 5 EXNESS\\terminal64.exe}"

# ini_set file section key value sets a key of an ini of the terminal, a
# UTF-16 text with a BOM and CRLF, created when missing: the terminal reads it
# at its start, and writes it back at its exit
ini_set() {
	local file="$1" section="$2" key="$3" value="$4" text=""
	[[ -f "$file" ]] && text="$(iconv -f UTF-16 -t UTF-8 "$file" | tr -d '\r' | sed '1s/^\xEF\xBB\xBF//')"
	text="$(printf '%s\n' "$text" | awk -v s="[$section]" -v k="$key" -v v="$value" '
		$0 == s { print; insec = 1; next }
		/^\[/ { if (insec && !done) { print k "=" v; done = 1 } insec = 0 }
		insec && index($0, k "=") == 1 { if (!done) print k "=" v; done = 1; next }
		NF { print }
		END { if (!done) { if (!insec) print s; print k "=" v } }')"
	{
		printf '\xFF\xFE'
		printf '%s\n' "$text" | sed 's/$/\r/' | iconv -f UTF-8 -t UTF-16LE
	} >"$file.tmp"
	mv "$file.tmp" "$file"
}

# the virtual display, before any program of Wine: its first one keeps the
# display it started with for good (run.sh finds this one running)
display="${MT5_DISPLAY:-:99}"
if ! pgrep -f "^Xvfb $display( |$)" >/dev/null; then
	rm -f "/tmp/.X11-unix/X${display#:}" "/tmp/.X${display#:}-lock"
	Xvfb "$display" -screen 0 1366x768x24 -nolisten tcp >/dev/null 2>&1 &
	for _ in $(seq 1 100); do
		[[ -S "/tmp/.X11-unix/X${display#:}" ]] && break
		sleep 0.1
	done
fi
export DISPLAY="$display"

# the VNC of the display, eg: to turn on the MCP server of the terminal once
if [[ -n "${MT5_VNC_PASSWORD:-}" ]]; then
	mkdir -p "$HOME/.vnc"
	x11vnc -storepasswd "$MT5_VNC_PASSWORD" "$HOME/.vnc/passwd" >/dev/null 2>&1
	x11vnc -display "$display" -rfbauth "$HOME/.vnc/passwd" -rfbport 5900 -forever -shared -quiet \
		-bg -o "$HOME/.vnc/x11vnc.log"
	log "VNC of the terminal on port 5900"
fi

# the prefix, without the Mono and Gecko of Wine, which would ask for them
if [[ ! -f "$WINEPREFIX/system.reg" ]]; then
	log "creating the Wine prefix $WINEPREFIX"
	WINEDLLOVERRIDES="mscoree,mshtml=" wineboot --init
	wineserver -w
fi

# the Windows Python of the bridge, embeddable, with its packages, offline:
# again after a start stopped in the middle, until its packages are there
if [[ ! -d "$python_dir/Lib/site-packages/MetaTrader5" ]]; then
	log "installing Python in $python_dir"
	mkdir -p "$python_dir"
	unzip -q -o "$downloads/python-embed.zip" -d "$python_dir"
	# the embeddable Python ignores its site-packages unless its ._pth says so
	printf 'python312.zip\r\n.\r\nLib\\site-packages\r\nimport site\r\n' >"$python_dir/python312._pth"
	# pip run from its wheel, which refuses to install itself when run as pip:
	# its main called from python -c instead
	pip_wheels=(/opt/xtrader/wheels/pip-*.whl)
	pip_wheel="$(winepath -w "${pip_wheels[0]}")"
	wheels="$(winepath -w /opt/xtrader/wheels)"
	wine "$python_dir/python.exe" -c "import sys; sys.path.insert(0, r'$pip_wheel')
from pip._internal.cli.main import main
sys.exit(main(['install', '--no-index', '--no-warn-script-location', '--find-links', r'$wheels',
               'pip', 'MetaTrader5', 'numpy']))"
	wineserver -w
fi

# the terminal of Exness: its web installer downloads it, silently (/auto),
# then opens it, closed here to be opened /portable by the bridge
if [[ ! -f "$terminal_dir/terminal64.exe" ]]; then
	installers=("$downloads"/*setup.exe)
	installer="${installers[0]}"
	log "installing the terminal with $(basename "$installer"): it downloads it, a few minutes"
	wine "$installer" /auto &
	installing=$!
	for _ in $(seq 1 180); do
		kill -0 "$installing" 2>/dev/null || break
		sleep 5
	done
	kill -0 "$installing" 2>/dev/null && log "the installer still runs after 15 minutes: closed"
	sleep 10
	wineserver -k || true
	wineserver -w || true
	if [[ ! -f "$terminal_dir/terminal64.exe" ]]; then
		log "no terminal in $terminal_dir: see the display with MT5_VNC_PASSWORD"
		exit 1
	fi
fi

# Algo Trading on and the Python API allowed ([Experts] of its common.ini),
# which trading from the bridge needs: its data in its folder, /portable. Kept
# on at a change of account (Account=0, its default turns it off then), which
# the first login of a new terminal is, or another MT5_LOGIN
if [[ "${MT5_ALGO_TRADING:-1}" != 0 ]]; then
	mkdir -p "$terminal_dir/Config"
	ini_set "$terminal_dir/Config/common.ini" Experts Enabled 1
	ini_set "$terminal_dir/Config/common.ini" Experts Api 0
	ini_set "$terminal_dir/Config/common.ini" Experts Account 0
fi

# the MCP server of the terminal (the economic calendar of the agents) listens
# on its loopback only: forwarded to the network of the compose, port 22347
if [[ "${MT5_MCP_FORWARD:-1}" != 0 ]]; then
	socat TCP-LISTEN:22347,fork,reuseaddr TCP:127.0.0.1:22346 &
fi

args=(--host 0.0.0.0 --portable)
[[ "${MT5_ALLOW_REAL:-0}" == 1 ]] && args+=(--allow-real)
[[ "${MT5_READ_ONLY:-0}" == 1 ]] && args+=(--read-only)
log "the bridge of the account ${MT5_LOGIN:-?} on ${MT5_SERVER:-?}"
exec /opt/xtrader/mt5bridge/run.sh "${args[@]}"
