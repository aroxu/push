# syntax=docker/dockerfile:1.7

# ---- stage 1: build the static frontend -------------------------------------
FROM node:22-alpine AS web
WORKDIR /app/web

COPY web/package.json web/package-lock.json ./
RUN npm ci --legacy-peer-deps --no-audit --no-fund

COPY web/ ./
RUN npm run build


# ---- stage 2: build the Go binary with the frontend embedded ----------------
FROM golang:1.24-alpine AS build
WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY server/ ./server/
COPY --from=web /app/web/out/ ./server/static/

# Fully static, stripped binary.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/push ./server


# ---- stage 3: minimal runtime ------------------------------------------------
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -g 10001 -S push && \
    adduser -u 10001 -S -G push push && \
    mkdir -p /data /var/log/push && chown push:push /data /var/log/push

COPY --from=build /out/push /usr/local/bin/push

USER push:push
EXPOSE 3234
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:3234/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/push"]
