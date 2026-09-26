# --- build stage ---
FROM golang:1.27-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ecr-vscode-proxy ./cmd/server

# --- runtime stage ---
FROM alpine:3.24

# Default to uid/gid 1000 so the container can read a bind-mounted host
# ~/.aws (owned by the host user, mode 0600); override at build time if
# your host user differs.
ARG APP_UID=1000
ARG APP_GID=1000

RUN addgroup -g "${APP_GID}" -S app && adduser -u "${APP_UID}" -S app -G app -h /home/app \
    && apk add --no-cache ca-certificates

COPY --from=builder /out/ecr-vscode-proxy /usr/local/bin/ecr-vscode-proxy

USER app
WORKDIR /home/app

EXPOSE 5000

ENTRYPOINT ["/usr/local/bin/ecr-vscode-proxy"]
