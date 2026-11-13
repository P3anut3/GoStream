FROM node:24-bookworm-slim AS frontend
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.1-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/gostream ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/gostream-worker ./cmd/worker

# Optional Linux verification target; the final runtime does not depend on it.
FROM builder AS checks
RUN go vet ./... && go test -race ./...

FROM debian:bookworm-slim

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/gostream ./gostream
# The worker shares this image; Compose starts it with its own entrypoint.
COPY --from=builder /out/gostream-worker ./gostream-worker
COPY --from=frontend /web/dist/ ./web/dist/
COPY internal/config/config.yaml ./internal/config/config.yaml

RUN mkdir -p uploads/videos uploads/covers \
    && chown -R 10001:10001 /app

ENV GIN_MODE=release \
    TZ=Asia/Shanghai \
    GOSTREAM_HTTP_ADDR=0.0.0.0:8080

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/gostream"]
