FROM golang:1.27.3-alpine AS builder

WORKDIR /src

ARG APP_VERSION

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X github.com/partyzanex/padmark/internal/buildinfo.version=${APP_VERSION}" \
      -o /bin/padmark-server \
      ./cmd/padmark-server && \
    # Pre-create /data owned by nobody (UID 65534) so the SQLite backend
    # can write padmark.db inside the scratch image (PADMARK_DSN=/data/padmark.db).
    # Not needed when using PostgreSQL.
    mkdir /data && chown nobody:nobody /data

# ------------------------------------------------------------
FROM scratch

# Run as an unprivileged user.
COPY --from=builder /etc/passwd /etc/passwd
# Root CAs let the server verify TLS peers, such as PostgreSQL with sslmode=verify-full.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER nobody

COPY --from=builder /bin/padmark-server /usr/local/bin/padmark-server
COPY --from=builder --chown=65534:65534 /data /data

VOLUME ["/data"]

WORKDIR /data

ENV PADMARK_DSN=/data/padmark.db \
    PADMARK_ADDR=:8080 \
    PADMARK_LOG_LEVEL=info \
    PADMARK_LOG_FORMAT=json

# No GOMEMLIMIT here: it must match the container's memory limit, which the image cannot know.
# Set it at run time to ~75-80% of that limit, e.g. `docker run --memory 256m -e GOMEMLIMIT=200MiB`,
# together with PADMARK_ARGON2_MEMORY_BUDGET (README: "Memory sizing (argon2id)").
# ENV GOMEMLIMIT=200MiB

EXPOSE 8080

ENTRYPOINT ["padmark-server"]
