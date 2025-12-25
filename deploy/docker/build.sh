#!/usr/bin/env bash
# Builds the images of the compose from this repository: xtrader-controller
# and xtrader-mt5, tagged as docker-compose.yml names them, its image: being
# the version of both (bump it there for a new one).
#
#	deploy/docker/build.sh              # both
#	deploy/docker/build.sh controller   # one
set -euo pipefail

here="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

# image returns the image of the service in the compose, eg: xtrader-mt5:1.0.0
image() {
	sed -nE "s/^[[:space:]]*image:[[:space:]]*(xtrader-$1:[^[:space:]]+).*/\1/p" "$here/docker-compose.yml" | head -1
}

services=("$@")
[[ ${#services[@]} -gt 0 ]] || services=(controller mt5)
for service in "${services[@]}"; do
	tag="$(image "$service")"
	if [[ -z "$tag" || ! -f "$here/images/$service/Dockerfile" ]]; then
		echo "build: no image of the service $service" >&2
		exit 1
	fi
	echo "build: $tag"
	DOCKER_BUILDKIT=1 docker build -f "$here/images/$service/Dockerfile" -t "$tag" "$repo"
done
