# syntax=docker/dockerfile:1.7

# ---- builder ----
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache deps separately from source for fast incremental builds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# CGO_ENABLED=0 + static build so the binary runs in scratch.
# -trimpath strips local paths from the binary; -s -w drops debug + symbols.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/server \
        ./cmd/server

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/server /server
COPY --from=builder /src/web/static /app/web/static

ENV LISTEN_ADDR=:8080 \
    STATIC_DIR=/app/web/static \
    HEALTHCHECK_URL=http://localhost:8080/healthz \
    LOG_LEVEL=info

EXPOSE 8080

USER nonroot:nonroot

# Healthcheck ships inside the binary itself, no wget/curl needed.
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=10s \
    CMD ["/server", "healthcheck"]

ENTRYPOINT ["/server"]
