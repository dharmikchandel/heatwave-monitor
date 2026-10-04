# syntax=docker/dockerfile:1
#
# One Dockerfile for all five Go services (weather, processing, risk, alert,
# gateway). Pick the service with --build-arg SERVICE=<name>.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first, so editing code does not re-download modules.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG SERVICE
RUN test -n "$SERVICE" || { echo "build with --build-arg SERVICE=<weather|processing|risk|alert|gateway>" >&2; exit 1; }
# Static binary (the SQLite driver is pure Go): no libc, no CGO, nothing to patch at runtime.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

FROM alpine:3.22
ARG SERVICE
ARG REVISION=unknown
LABEL org.opencontainers.image.title="heatwave-monitor ${SERVICE}" \
      org.opencontainers.image.source="https://github.com/dharmikchandel/heatwave-monitor" \
      org.opencontainers.image.revision="${REVISION}"

# Run as an unprivileged user; /data is the only place the service may write.
RUN addgroup -S -g 10001 app && adduser -S -u 10001 -G app -H -s /sbin/nologin app \
 && mkdir /data && chown app:app /data
COPY --from=build /out/service /usr/local/bin/service

USER 10001:10001
WORKDIR /data
VOLUME ["/data"]

# Every service listens on $PORT and answers /readyz (503 until its database is usable).
ENV PORT=8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/readyz" || exit 1
ENTRYPOINT ["service"]
