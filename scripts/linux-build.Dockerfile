FROM node:24-bookworm AS node
FROM golang:1.25-bookworm AS go
FROM rust:1.96-bookworm AS rust
FROM ubuntu:22.04 AS build

ENV DEBIAN_FRONTEND=noninteractive \
    APPIMAGE_EXTRACT_AND_RUN=1 \
    CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

COPY --from=node /usr/local/ /usr/local/
COPY --from=go /usr/local/go/ /usr/local/go/
COPY --from=rust /usr/local/cargo/ /usr/local/cargo/
COPY --from=rust /usr/local/rustup/ /usr/local/rustup/
COPY --from=node /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

RUN sed -i 's|http://|https://|g' /etc/apt/sources.list \
    && apt-get -o Acquire::Retries=5 -o Acquire::https::Timeout=30 update \
    && apt-get install -y --no-install-recommends \
      build-essential \
      ca-certificates \
      file \
      libayatana-appindicator3-dev \
      libfuse2 \
      librsvg2-dev \
      libwebkit2gtk-4.1-dev \
      patchelf \
      pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN corepack enable pnpm \
    && corepack prepare pnpm@10.26.2 --activate

WORKDIR /src
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY frontend/package.json frontend/package.json
RUN pnpm install --frozen-lockfile

COPY backend/go.mod backend/go.sum backend/
RUN cd backend \
    && for attempt in 1 2 3; do go mod download && exit 0; sleep $((attempt * 3)); done \
    && exit 1

COPY src-tauri/Cargo.toml src-tauri/Cargo.lock src-tauri/
RUN mkdir -p src-tauri/src \
    && : > src-tauri/src/lib.rs \
    && cd src-tauri \
    && for attempt in 1 2 3; do cargo fetch --locked && exit 0; sleep $((attempt * 3)); done \
    && exit 1

COPY . .
RUN pnpm build

FROM scratch AS release
COPY --from=build /src/artifacts/release/ /
