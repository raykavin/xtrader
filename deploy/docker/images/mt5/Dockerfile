# syntax=docker/dockerfile:1
#
# The MetaTrader 5 terminal of Exness under Wine, drawn on a virtual display
# (Xvfb), and tools/mt5bridge serving it to the agents over HTTP. The Wine
# prefix lives in the volume /data: the first start installs the Windows
# Python and the terminal into it (see entrypoint.sh), the next ones reuse it,
# the terminal logged in and updated. Built by deploy/docker/build.sh, the
# context the repository.

# the packages of the bridge for the Windows Python, downloaded once here: the
# prefix installs them offline
FROM python:3.12.14-slim-trixie AS wheels
RUN pip download --no-cache-dir --only-binary=:all: --platform win_amd64 --python-version 3.12 \
      --implementation cp --abi cp312 --dest /wheels \
      pip==26.2.1 MetaTrader5==5.0.6180 numpy==1.26.4

FROM debian:13.7-slim

# the embeddable Python of the bridge, as on the terminals running it today,
# and the web installer of the terminal of Exness (a 64-bit program: no 32-bit
# Wine), which downloads the terminal itself
ARG PYTHON_VERSION=3.12.8
ARG PYTHON_SHA256=8d3f33be9eb810f23c102f08475af2854e50484b8e4e06275e937be61ce3d2fb
ARG MT5_INSTALLER_URL=https://download.mql5.com/cdn/web/exness.technologies.ltd/mt5/exness5setup.exe
# the user of Wine: the prefix belongs to it
ARG UID=1000
ARG GID=1000

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      wine wine64 xvfb x11vnc socat curl ca-certificates tini gosu unzip procps tzdata \
 && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid "$GID" trader \
 && useradd --uid "$UID" --gid trader --create-home --shell /bin/bash trader

RUN mkdir -p /opt/xtrader/downloads \
 && curl -fsSL -o /opt/xtrader/downloads/python-embed.zip \
      "https://www.python.org/ftp/python/${PYTHON_VERSION}/python-${PYTHON_VERSION}-embed-amd64.zip" \
 && echo "${PYTHON_SHA256}  /opt/xtrader/downloads/python-embed.zip" | sha256sum -c - \
 && curl -fsSL -o "/opt/xtrader/downloads/$(basename "$MT5_INSTALLER_URL")" "$MT5_INSTALLER_URL"

COPY --from=wheels /wheels/ /opt/xtrader/wheels/
COPY tools/mt5bridge/ /opt/xtrader/mt5bridge/
COPY deploy/docker/images/mt5/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh /opt/xtrader/mt5bridge/run.sh

ENV WINEPREFIX=/data/wine \
    WINEARCH=win64 \
    WINEDEBUG=-all \
    MT5_DISPLAY=:99

# 18812 the bridge, 22347 the MCP server of the terminal forwarded, 5900 the VNC
EXPOSE 18812 22347 5900

# the bridge closes its connection to the terminal on Ctrl+C
STOPSIGNAL SIGINT
ENTRYPOINT ["tini", "-g", "--", "/usr/local/bin/entrypoint.sh"]
