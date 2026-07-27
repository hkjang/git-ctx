FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go test ./... && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/git-ctx ./cmd/git-ctx

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
RUN useradd --system --uid 10001 --home /app gitctx && mkdir -p /var/lib/git-ctx/backups && chown 10001:10001 /var/lib/git-ctx/backups
WORKDIR /app
COPY --from=build /out/git-ctx /app/git-ctx
COPY web /app/web
USER 10001
ENV GIT_CTX_BACKUP_DIR=/var/lib/git-ctx/backups
EXPOSE 4747
ENTRYPOINT ["/app/git-ctx"]
