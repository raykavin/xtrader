#!/usr/bin/env bash
# Rebuilds the images of the compose, asking which ones (the controller, MT5 or
# both, built by build.sh from this repository as it is, its uncommitted
# changes included), then whether to recreate the containers of the project
# installed (see install.sh) with docker compose up -d --force-recreate.
#
#	deploy/docker/rebuild.sh [--dry-run] [/var/containers/xtrader]
#	make docker-rebuild
#
# --dry-run asks the same, then prints the commands without running them.
set -euo pipefail

here="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
dry=false
target=/var/containers/xtrader
for arg in "$@"; do
	case "$arg" in
	--dry-run) dry=true ;;
	-h | --help)
		sed -n '2,10s/^# \{0,1\}//p' "$0"
		exit 0
		;;
	-*)
		echo "rebuild: unknown option $arg" >&2
		exit 2
		;;
	*) target="$arg" ;;
	esac
done

# run prints a command, then runs it, unless --dry-run
run() {
	printf '+ %s\n' "$*"
	if ! $dry; then
		"$@"
	fi
}

# image prints the image of the service in the compose, eg: xtrader-mt5:1.0.0,
# as build.sh tags it
image() {
	sed -nE "s/^[[:space:]]*image:[[:space:]]*(xtrader-$1:[^[:space:]]+).*/\1/p" "$here/docker-compose.yml" | head -1
}

# ask prints the question and reads the answer into the variable named $1
ask() {
	local -n answer="$1"
	if ! read -r -p "$2" answer; then
		echo
		echo "rebuild: no answer: stopped" >&2
		exit 1
	fi
}

branch="$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")"
commit="$(git -C "$repo" log -1 --format='%h %s' 2>/dev/null || echo "?")"
echo "Repositório: $repo"
echo "Código: $branch @ $commit"
if [[ -n "$(git -C "$repo" status --porcelain 2>/dev/null)" ]]; then
	echo "Atenção: há alterações não commitadas, e elas entram nas imagens."
fi
echo
echo "Imagens do compose:"
echo "  1) controller  $(image controller)  painel, agentes e migrações (também a do migrator)"
echo "  2) mt5         $(image mt5)  terminal MetaTrader 5 e bridge"
echo "  3) as duas"
echo "  0) nenhuma: só reiniciar o compose"

services=()
while true; do
	ask choice "Quais imagens reconstruir? [1, 2, 3 ou 0; ex.: 1 2]: "
	services=()
	valid=true
	for option in ${choice//,/ }; do
		case "$option" in
		1) services+=(controller) ;;
		2) services+=(mt5) ;;
		3) services+=(controller mt5) ;;
		0) ;;
		*) valid=false ;;
		esac
	done
	if $valid && [[ -n "${choice// /}" ]]; then
		break
	fi
	echo "Resposta inválida: use 1, 2, 3 ou 0."
done
# each image once, in the order of build.sh
mapfile -t services < <(printf '%s\n' "${services[@]}" | awk 'NF && !seen[$0]++')

if ((${#services[@]} > 0)); then
	echo
	run "$here/build.sh" "${services[@]}"
	if ! $dry; then
		for service in "${services[@]}"; do
			docker image ls --format '  {{.Repository}}:{{.Tag}}  {{.ID}}  {{.CreatedSince}}' "$(image "$service")"
		done
	fi
else
	echo "Nenhuma imagem reconstruída."
fi

compose_file="$target/docker-compose.yml"
if [[ ! -f "$compose_file" ]]; then
	echo
	echo "Nenhum projeto instalado em $target (sem docker-compose.yml): nada a reiniciar."
	echo "Instale com deploy/docker/install.sh, ou passe o diretório: rebuild.sh /outro/diretorio"
	# the images built are no failure; asked to restart only, it is
	((${#services[@]} > 0))
	exit
fi
compose=(docker compose -f "$compose_file" --project-directory "$target")

echo
echo "Reiniciar recria todos os serviços do projeto em $target (postgres, mt5, migrator e"
echo "controller): os agentes param e voltam, o terminal do MT5 reinicia e o migrator aplica"
echo "as migrações novas. Prefira fazê-lo sem posição aberta."
ask restart "Reiniciar agora com 'docker compose up -d --force-recreate'? [s/N]: "
case "${restart,,}" in
s | sim | y | yes)
	run "${compose[@]}" up -d --force-recreate
	run "${compose[@]}" ps
	;;
*)
	echo "Compose não reiniciado. Para aplicar depois:"
	echo "  docker compose -f $compose_file --project-directory $target up -d --force-recreate"
	;;
esac
