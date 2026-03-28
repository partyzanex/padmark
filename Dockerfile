FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY . .

RUN CGO_ENABLED=0 go build \
      -mod=vendor \
      -trimpath \
      -ldflags="-s -w" \
      -o /bin/server \
      ./cmd/server && \
    mkdir /data && chown nobody:nobody /data

# ------------------------------------------------------------
FROM scratch

# Run as an unprivileged user.
COPY --from=builder /etc/passwd /etc/passwd
USER nobody

COPY --from=builder /bin/server /server
COPY --from=builder --chown=65534:65534 /data /data

VOLUME ["/data"]

ENV PADMARK_DSN=/data/padmark.db \
    PADMARK_ADDR=:8080 \
    PADMARK_LOG_LEVEL=info \
    PADMARK_LOG_FORMAT=json

EXPOSE 8080

ENTRYPOINT ["/server"]
