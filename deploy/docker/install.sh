#!/usr/bin/env bash
# Installs the project in the directory of the containers of the server,
# /var/containers/xtrader by default: the compose, and the directories of its
# services, keeping what is there already (the .env, the data, the config).
# The .env of each service comes from its .env.example, and its secrets are
# generated once, the same where each is needed: the token of the bridge (mt5
# only), the password of the database (postgres, migrator and controller) and
# the key of the secrets of the database (migrator and controller).
#
#	sudo deploy/docker/install.sh [/var/containers/xtrader]
set -euo pipefail

here="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
target="${1:-/var/containers/xtrader}"

# set_env file key value sets a variable of an env file
set_env() {
	local file="$1" key="$2" value="$3"
	if grep -qE "^$key=" "$file"; then
		sed -i "s|^$key=.*|$key=$value|" "$file"
	else
		printf '%s=%s\n' "$key" "$value" >>"$file"
	fi
}

mkdir -p "$target"
if [[ -f "$target/docker-compose.yml" ]] && ! cmp -s "$here/docker-compose.yml" "$target/docker-compose.yml"; then
	cp "$target/docker-compose.yml" "$target/docker-compose.yml.bak"
	echo "install: the previous docker-compose.yml kept as docker-compose.yml.bak"
fi
install -m 644 "$here/docker-compose.yml" "$target/docker-compose.yml"

# get_env file key prints the value of a variable of an env file, if any
get_env() {
	sed -n "s/^$2=//p" "$1" | head -1
}

# secret key file... prints the value of the variable key in the first of the
# env files that has it, else a new random one of hex characters (safe in a
# URL, eg: the DSN of the database)
secret() {
	local key="$1" value file
	shift
	for file in "$@"; do
		value="$(get_env "$file" "$key")"
		if [[ -n "$value" ]]; then
			printf '%s' "$value"
			return
		fi
	done
	head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

for service in controller migrator mt5 postgres; do
	mkdir -p "$target/services/$service"
	if [[ ! -f "$target/services/$service/.env" ]]; then
		install -m 600 "$here/services/$service/.env.example" "$target/services/$service/.env"
		echo "install: services/$service/.env created"
	fi
done
for service in controller mt5 postgres; do
	mkdir -p "$target/services/$service/data"
done
# the user postgres of its container (uid 999) goes through the volume to its
# data, which the entrypoint creates in it and keeps to itself (0700): a
# volume of 0700, eg: of a umask of 077, stops postgres from starting
chmod 755 "$target/services/postgres/data"
# the configurations: the ones there already kept
for file in controller/config/config.yml migrator/config/config.yml; do
	mkdir -p "$(dirname "$target/services/$file")"
	if [[ ! -f "$target/services/$file" ]]; then
		install -m 644 "$here/services/$file" "$target/services/$file"
	fi
done
env_of() { printf '%s' "$target/services/$1/.env"; }

# the token of the bridge: the one of mt5, else a new one; the agents have it
# in the database (the settings of the dashboard), not in the environment of
# the controller, where an install before kept it
token="$(secret MT5_BRIDGE_TOKEN "$(env_of mt5)" "$(env_of controller)")"
set_env "$(env_of mt5)" MT5_BRIDGE_TOKEN "$token"
sed -i '/^MT5_BRIDGE_TOKEN=/d' "$(env_of controller)"

# the password of the database: the one of postgres, else a new one
password="$(secret POSTGRES_PASSWORD "$(env_of postgres)")"
set_env "$(env_of postgres)" POSTGRES_PASSWORD "$password"
for service in controller migrator; do
	set_env "$(env_of "$service")" XTRADER_DB_PASSWORD "$password"
done

# the key of the secrets: the one of the controller or of the migrator, else a
# new one of 32 bytes in base64
key="$(get_env "$(env_of controller)" XTRADER_SECRETS_KEY)"
[[ -n "$key" ]] || key="$(get_env "$(env_of migrator)" XTRADER_SECRETS_KEY)"
if [[ -z "$key" ]]; then
	key="$(head -c 32 /dev/urandom | base64)"
	echo "install: a key of the secrets generated: keep a copy of services/controller/.env"
fi
for service in controller migrator; do
	set_env "$(env_of "$service")" XTRADER_SECRETS_KEY "$key"
done

# the data of the containers belongs to their user (uid 1000); their
# entrypoints fix it too, and the one of postgres its own data
if [[ "$(id -u)" == 0 ]]; then
	chown -R 1000:1000 "$target/services/controller/data" "$target/services/mt5/data"
fi

echo "install: $target ready; fill in the first user of the dashboard in the .env of the controller"
echo "(XTRADER_BOOTSTRAP_USERNAME and XTRADER_BOOTSTRAP_PASSWORD), then: cd $target && docker compose up -d;"
echo "then add the account of MT5 in the dashboard (Configurações > Contas MT5), with the MT5_BRIDGE_TOKEN of the"
echo ".env of mt5 as the token of its bridge, and activate it"
echo "install: the ports are published on the loopback of this host only (the dashboard on http://127.0.0.1:8082,"
echo "VNC on 5900, PostgreSQL on 5432): from another machine, through an SSH tunnel (see deploy/docker/README.md)"
