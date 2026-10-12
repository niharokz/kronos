# ── Build ────────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go vet ./... \
 && CGO_ENABLED=0 go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kronos ./cmd/kronos

# ── Run ──────────────────────────────────────────────────────────────────
# Alpine (not scratch) so script jobs have /bin/sh and basic tools.
# Add packages your scripts need here, e.g.: RUN apk add --no-cache python3
FROM alpine:3.20
ARG VERSION=dev
LABEL org.opencontainers.image.title="kronos" \
      org.opencontainers.image.description="Small modular job scheduler for a self-hosted notes vault" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/kronos /app/kronos
ENV PATH="/app:${PATH}"
# Non-root by default; docker-compose.yml overrides it with PUID:PGID from .env.
USER 1000:1000
CMD ["kronos", "serve"]
