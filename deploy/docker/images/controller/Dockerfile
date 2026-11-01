# syntax=docker/dockerfile:1
#
# The controller of xtrader (cmd/controller): the web dashboard, the Telegram,
# and the agents of its database it launches and supervises, from their
# binaries in /app/bin, their data in /app/data (the volume); and the migrator
# (cmd/migrator) of the database, its SQL in /app/sql, run by the service
# migrator before the controller. Built by deploy/docker/build.sh, the context
# the repository.

# the dashboard (web/): its bundle, served from /app/web
FROM oven/bun:1.4.2-slim AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

# the controller and the agents, static binaries
FROM golang:1.26.8-trixie AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ \
      ./cmd/controller \
      ./cmd/migrator \
      ./cmd/xtrader \
      ./agents/llm/llmagent \
      ./agents/trendagent \
      ./agents/momentumagent \
      ./agents/breakoutagent \
      ./agents/intradayagent

FROM debian:13.7-slim

# the user of the controller and of the agents: the data belongs to it
ARG UID=1000
ARG GID=1000

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tini gosu tzdata \
 && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid "$GID" trader \
 && useradd --uid "$UID" --gid trader --create-home --shell /bin/bash trader

WORKDIR /app
COPY --from=go /out/ /app/bin/
COPY --from=web /src/web/dist/ /app/web/
COPY sql/ /app/sql/
COPY deploy/docker/images/controller/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh && mkdir -p /app/data && chown trader:trader /app/data

ENV PATH=/app/bin:$PATH

# 8080 the dashboard; the API of each agent (eg: 5580) stays inside
EXPOSE 8080

ENTRYPOINT ["tini", "-g", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["-config", "/app/config/config.yml"]
