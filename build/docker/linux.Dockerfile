FROM golang:1.26-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
