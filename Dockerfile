# syntax=docker/dockerfile:1.7
#
# Multi-stage build for BidloBot.
#
# Stage 1 ("build"): golang:1.26-alpine. CGO_ENABLED=0 because every
# dependency (telego, bbolt, uuid, golang.org/x/text) is pure Go - keeps
# the runtime image free of glibc and musl headaches.
#
# Stage 2 ("runtime"): debian:bookworm-slim. Picked over distroless
# because operators occasionally need to docker exec for health
# debugging; tini is included as PID 1 so SIGTERM reaches the Go runtime
# without the "PID 1 ignores signals" quirk.
#
# The single bidlobot binary ships in the runtime image; backups are a
# host-side `cp` job (scripts/backup.sh) because bbolt holds an
# exclusive flock while the bot is running.

FROM golang:1.26-alpine AS build

WORKDIR /src

RUN apk add --no-cache git



COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath go build \
        -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/bidlobot ./cmd/bidlobot

FROM debian:bookworm-slim AS runtime
# Pinned to 2026.03.17: TikTok changed anti-bot on 2026-08-10 and broke the
# extractor in newer releases (yt-dlp issue #17403, still open). The old
# extractor still works. Bump back once upstream ships a fix.
ARG YT_DLP_VERSION=2026.03.17


RUN apt-get update && \
    apt-get install -y --no-install-recommends bash ca-certificates curl ffmpeg tini tzdata unzip wget && \
    rm -rf /var/lib/apt/lists/* && \
    curl -fsSL "https://github.com/yt-dlp/yt-dlp/releases/download/${YT_DLP_VERSION}/yt-dlp_linux" \
        -o /tmp/yt-dlp && \
    echo "c2b0189f581fe4a2ddd41954f1bcb7d327db04b07ed0dea97e4f1b3e09b5dd8e  /tmp/yt-dlp" | sha256sum -c - && \
    install -m 0755 /tmp/yt-dlp /usr/local/bin/yt-dlp && \
    rm /tmp/yt-dlp && \
    export BUN_INSTALL=/opt/bun && \
    export PATH="$BUN_INSTALL/bin:$PATH" && \
    curl -fsSL https://bun.sh/install | bash -s -- bun-v1.3.14 && \
    bun install -g @oh-my-pi/pi-coding-agent@16.3.6 && \
    omp --version && \
    yt-dlp --version && \
    groupadd --system --gid 65532 bidlobot && \
    useradd --system --uid 65532 --gid bidlobot --home-dir /var/lib/bidlobot --shell /usr/sbin/nologin bidlobot && \
    install -d -o bidlobot -g bidlobot -m 0750 /var/lib/bidlobot && \
    install -d -o bidlobot -g bidlobot -m 0750 /var/lib/bidlobot/backups && \
    install -o bidlobot -g bidlobot -m 0640 /dev/null /var/lib/bidlobot/.keep

COPY --from=build /out/bidlobot /usr/local/bin/bidlobot

USER bidlobot:bidlobot
WORKDIR /var/lib/bidlobot

ENV BUN_INSTALL=/opt/bun \
    PATH=/opt/bun/bin:${PATH} \
    DB_PATH=/var/lib/bidlobot \
    LOG_LEVEL=info

EXPOSE 8080

# Internal health probe. The health port is never published to the host
# (compose owns that decision); it is reached only over the loopback
# inside the container, so wget --spider is a tight, dep-free probe.
# start-period: 60s absorbs slow Telegram cold start (GetMe can
# take 10s+ during regional API blips) plus bbolt open and bucket init.
HEALTHCHECK --interval=30s --timeout=3s --start-period=60s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/bidlobot"]
