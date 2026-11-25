# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1: build a static Go binary.
# ---------------------------------------------------------------------------
FROM golang:1.27.4-alpine AS builder

WORKDIR /src

# Download modules in their own layer: it is only rebuilt when go.mod or
# go.sum change, which keeps code-only rebuilds fast.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# CGO_ENABLED=0  -> fully static binary, runs on an empty (scratch) image
# -trimpath      -> strip local file paths from the binary (reproducible builds)
# -ldflags -s -w -> drop the symbol table and debug info (smaller binary)
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kvcache ./cmd/kvcache

# ---------------------------------------------------------------------------
# Stage 2: runtime image with nothing but the binary.
# `scratch` has no shell, package manager or libc, so there is almost nothing
# for an attacker to use and nothing for vulnerability scanners to flag.
# ---------------------------------------------------------------------------
FROM scratch

COPY --from=builder /out/kvcache /kvcache

# Run as an unprivileged, non-root user (numeric IDs work without /etc/passwd).
USER 65532:65532

EXPOSE 7171

# The binary probes itself, so no curl/wget is needed in the image.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/kvcache", "-healthcheck"]

ENTRYPOINT ["/kvcache"]
