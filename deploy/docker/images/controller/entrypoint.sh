#!/usr/bin/env bash
# The start of the container of the controller. As root: the data (/app/data,
# the volume) given to the user of the agents, eg: copied from another
# machine, then the rest as it: the controller, which launches the agents of
# its database from /app, their data in /app/data; or the migrator of the
# database, its first argument "migrator", eg: of the service migrator.
set -euo pipefail

user=trader
if [[ "$(id -u)" == 0 ]]; then
	mkdir -p /app/data
	if [[ "$(stat -c %u /app/data)" != "$(id -u "$user")" ]]; then
		echo "controller: /app/data given to $user" >&2
		chown -R "$user:$user" /app/data
	fi
	exec gosu "$user" "$0" "$@"
fi

program=controller
if [[ "${1:-}" == migrator ]]; then
	program=migrator
	shift
fi
exec "$program" "$@"
