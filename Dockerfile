# syntax=docker/dockerfile:1.7
#
# Multi-stage build for BidloBot.
#
# Stage 1 ("build"): golang:1.26-alpine. CGO_ENABLED=0 because every
# dependency (telego, bbolt, uuid, golang.org/x/text) is pure Go - keeps
# the runtime image free of glibc and musl headaches.
#
# Stage 2 ("runtime"): alpine:3.20. Picked over distroless because
# operators occasionally need to docker exec for the backup binary and
# health debugging; tini is included as PID 1 so SIGTERM reaches the
# Go runtime without the "PID 1 ignores signals" quirk.
#
# All three binaries (bidlobot, bidlobot-backup, bidlobot-probe) ship in
# the same image so a single `docker exec bidlobot bidlobot-backup ...`
# is enough to rotate snapshots without a sidecar.

ARG GO_VERSION=1.26-alpine
ARG ALPINE_VERSION=3.20

FROM golang:${GO_VERSION} AS build

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
        -o /out/bidlobot ./cmd/bidlobot && \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath go build \
        -ldflags "-s -w" \
        -o /out/bidlobot-backup ./cmd/bidlobot-backup && \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath go build \
        -ldflags "-s -w" \
        -o /out/bidlobot-probe ./cmd/probe

FROM alpine:${ALPINE_VERSION} AS runtime

RUN apk add --no-cache ca-certificates tzdata wget tini ffmpeg yt-dlp && \
    addgroup -S -g 65532 bidlobot && \
    adduser -S -u 65532 -G bidlobot -h /var/lib/bidlobot -s /sbin/nologin bidlobot && \
    install -d -o bidlobot -g bidlobot -m 0750 /var/lib/bidlobot && \
    install -d -o bidlobot -g bidlobot -m 0750 /var/lib/bidlobot/backups && \
    # Marker file forces Docker to copy the image-baked ownership of
    # /var/lib/bidlobot into a fresh named volume on first start.
    # Without this, an empty named volume defaults to root:root 0755
    # and the unprivileged bidlobot user fails on first bbolt.Open.
    install -o bidlobot -g bidlobot -m 0640 /dev/null /var/lib/bidlobot/.keep

COPY --from=build /out/bidlobot         /usr/local/bin/bidlobot
COPY --from=build /out/bidlobot-backup  /usr/local/bin/bidlobot-backup
COPY --from=build /out/bidlobot-probe   /usr/local/bin/bidlobot-probe

USER bidlobot:bidlobot
WORKDIR /var/lib/bidlobot

ENV DB_PATH=/var/lib/bidlobot \
    HEALTH_PORT=8080 \
    LOG_LEVEL=info

EXPOSE 8080

# Internal health probe. The container's HEALTH_PORT is never published
# to the host (compose owns that decision); it is reached only over the
# loopback inside the container, so wget --spider is a tight, dep-free
# probe. start-period: 60s absorbs slow Telegram cold start (GetMe can
# take 10s+ during regional API blips) plus bbolt open and bucket init.
HEALTHCHECK --interval=30s --timeout=3s --start-period=60s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/bidlobot"]
