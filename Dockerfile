# ── Build ────────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kronos ./cmd/kronos \
 && CGO_ENABLED=0 go test ./...

# ── Run ──────────────────────────────────────────────────────────────────
# Alpine (not scratch) so script jobs have /bin/sh and basic tools.
# Add packages your scripts need here, e.g.: RUN apk add --no-cache python3
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/kronos /app/kronos
ENV PATH="/app:${PATH}"
CMD ["kronos", "serve"]
