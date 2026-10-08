.PHONY: generate generate-check lint test bridge-test release ui-install ui-build ui-dev ui-check ui-bundle

generate:
	go generate ./...

# Fails when web/src/api/types.ts is out of date with ui/dto.go.
generate-check:
	cd ui && go generate .
	git diff --exit-code -- web/src/api/types.ts

lint:
	golangci-lint run --fix

test:
	go test -race -cover ./...

# The tests of tools/mt5bridge, with a MetaTrader5 package simulated by the
# tests themselves (never the real one); -B leaves no __pycache__.
bridge-test:
	python3 -B -m unittest discover -s tools/mt5bridge -v

release:
	goreleaser build --snapshot

# --- web dashboard (requires bun: https://bun.sh) ---

ui-install:
	cd web && bun install --frozen-lockfile

ui-dev:
	cd web && bun run dev

ui-check:
	cd web && bun run lint && bun run typecheck && bun run test

ui-build:
	cd web && bun run build

# Creates the release asset served by the Go binary (see controller/bundle.go).
ui-bundle: ui-build
	tar -czf web/xtrader-ui.tar.gz -C web/dist .
