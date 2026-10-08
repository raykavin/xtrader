#!/usr/bin/env bash
# Runs the controller of a configuration apart from the terminal, in a session
# of its own: closing the terminal, or VS Code, doesn't stop it, nor the bridge
# of MT5 and the agents it launches. The migrations of the database run first
# (cmd/migrator up). The ${VAR} of the configuration are read from the
# environment of the shell: export them first, see .env.example.
#
#	tools/automator.sh start configs/config.yml
#	tools/automator.sh status configs/config.yml
#	tools/automator.sh logs configs/config.yml     # follows the log, Ctrl+C leaves it
#	tools/automator.sh stop configs/config.yml     # stops the agents, then the bridge
#
# The log and the pid are in ${XDG_STATE_HOME:-~/.local/state}/automator.
#
# stop waits for the controller as long as its stop takes, the shutdown_timeout
# of the configuration at most (which kills what is left past it), up to
# AUTOMATOR_STOP_TIMEOUT seconds (300 by default: keep it above
# shutdown_timeout); then whatever the controller launched still runs, eg: once
# it was killed, is stopped too.
set -euo pipefail

repo="$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)"
state="${XDG_STATE_HOME:-$HOME/.local/state}/automator"

usage() {
	echo "usage: $0 start|stop|status|logs <config.yml>" >&2
	exit 2
}

[[ $# -eq 2 ]] || usage
action="$1"
config="$(readlink -f "$2")"
name="$(basename "$config" .yml)"
pidfile="$state/$name.pid"
logfile="$state/$name.log"

running() {
	[[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null
}

# leftovers prints the processes of the session $1 the controller launched, of
# the groups of their own of the agents and of the services: the ones marked
# XTRADER_LAUNCH in their environment (see controller/pids.go)
leftovers() {
	local p
	for p in $(pgrep -s "$1" 2>/dev/null); do
		grep -qz '^XTRADER_LAUNCH=' "/proc/$p/environ" 2>/dev/null && echo "$p"
	done
	return 0
}

# stop_leftovers stops the leftovers of the session $1: interrupted, as the
# controller stops them, then killed after 20 seconds
stop_leftovers() {
	local left
	left="$(leftovers "$1")"
	[[ -n "$left" ]] || return 0
	echo "automator: processes of the controller still running, stopped:" $left >&2
	# shellcheck disable=SC2086 # one PID each
	kill -INT $left 2>/dev/null || true
	for _ in $(seq 1 20); do
		left="$(leftovers "$1")"
		[[ -n "$left" ]] || return 0
		sleep 1
	done
	echo "automator: still running after 20s, killed:" $left >&2
	# shellcheck disable=SC2086
	kill -KILL $left 2>/dev/null || true
}

case "$action" in
start)
	[[ -f "$config" ]] || { echo "automator: no $config" >&2; exit 1; }
	if running; then
		echo "automator: $name runs already (pid $(cat "$pidfile")), see: $0 logs $2" >&2
		exit 1
	fi
	mkdir -p "$state"
	cd "$repo"
	# the schema and the seeds of the database, as the controller checks them
	go run ./cmd/migrator -config "$config" up >>"$logfile" 2>&1 ||
		{ echo "automator: the migrations failed, see $logfile" >&2; exit 1; }
	# setsid: a session of its own, which the hangup of the terminal doesn't reach
	setsid nohup go run ./cmd/controller -config "$config" >>"$logfile" 2>&1 </dev/null &
	echo $! >"$pidfile"
	echo "automator: $name started (pid $!), dashboard at the listen of $2, log $logfile"
	;;
stop)
	if ! running; then
		echo "automator: $name isn't running"
		# a controller that died may have left its processes running
		[[ -f "$pidfile" ]] && stop_leftovers "$(cat "$pidfile")"
		rm -f "$pidfile"
		exit 0
	fi
	pid="$(cat "$pidfile")"
	wait="${AUTOMATOR_STOP_TIMEOUT:-300}"
	# the group of the session: `go run` and the controller it runs, which stops
	# its agents and then its services, within its shutdown_timeout
	kill -INT -- "-$pid"
	for _ in $(seq 1 "$wait"); do
		kill -0 "$pid" 2>/dev/null || break
		sleep 1
	done
	if kill -0 "$pid" 2>/dev/null; then
		echo "automator: $name still running after ${wait}s, killed" >&2
		kill -KILL -- "-$pid" 2>/dev/null || true
	fi
	# the agents and the services run in groups of their own, of the session
	stop_leftovers "$pid"
	rm -f "$pidfile"
	echo "automator: $name stopped"
	;;
status)
	if running; then
		echo "automator: $name running (pid $(cat "$pidfile")), log $logfile"
	else
		echo "automator: $name stopped"
		exit 1
	fi
	;;
logs)
	exec tail -n 100 -F "$logfile"
	;;
*)
	usage
	;;
esac
