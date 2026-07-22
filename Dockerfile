# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.25-bookworm AS build

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Build the statically-linked server binary. The web client is embedded via
# go:embed, so no extra assets are needed at runtime.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage ----
FROM debian:bookworm-slim

# ffmpeg/ffprobe are required for metadata extraction and thumbnail generation.
RUN apt-get update -qq && \
    apt-get install --no-install-recommends -y \
      ca-certificates \
      ffmpeg \
      python3 \
      python3-opencv && \
    rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

WORKDIR /app
COPY --from=build /out/server /usr/local/bin/server

ENV PORT=8000 \
    DATA_DIR=/data

RUN groupadd --gid 1000 app && \
    useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin app && \
    mkdir -p /data && chown -R app:app /data
USER 1000:1000

VOLUME ["/data"]
EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/server", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/server"]
