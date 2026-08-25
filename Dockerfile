FROM golang:1.26-bookworm AS build
WORKDIR /src
# VERSION 은 릴리스 태그입니다. 소스의 version.Version 과 다르면 빌드를 멈춰,
# 태그만 올리고 코드 버전을 잊는 배포 사고를 원천 차단합니다.
ARG VERSION=""
ARG COMMIT=""
ARG BUILD_TIME=""
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
# 이미지 안에서도 전체 시험이 돌기 때문에, 공개 API 계약을 읽는 시험이 읽을 수
# 있도록 명세 파일 하나를 함께 가져옵니다. 문서 전체를 넣으면 빌드 컨텍스트만
# 커지고 시험에는 쓰이지 않습니다.
COPY docs/openapi.yaml ./docs/openapi.yaml
RUN set -eu; \
    source_version=$(sed -n 's/.*Version = "\(.*\)".*/\1/p' internal/version/version.go); \
    if [ -n "$VERSION" ] && [ "${VERSION#v}" != "$source_version" ]; then \
      echo "이미지 태그($VERSION)와 소스 버전($source_version)이 다릅니다" >&2; exit 1; \
    fi
RUN CGO_ENABLED=1 go test -tags sqlite_fts5 ./... && CGO_ENABLED=1 go build -tags sqlite_fts5 -trimpath \
      -ldflags="-s -w -X git-ctx/internal/version.Commit=${COMMIT} -X git-ctx/internal/version.BuildTime=${BUILD_TIME}" \
      -o /out/git-ctx ./cmd/git-ctx

FROM debian:bookworm-slim
ARG VERSION=""
ARG COMMIT=""
ARG BUILD_TIME=""
LABEL org.opencontainers.image.title="git-ctx" \
      org.opencontainers.image.description="BitContext-compatible internal development knowledge platform" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.source="https://github.com/hkjang/git-ctx"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
RUN useradd --system --uid 10001 --home /app gitctx && mkdir -p /var/lib/git-ctx/backups && chown -R 10001:10001 /var/lib/git-ctx
WORKDIR /var/lib/git-ctx
COPY --from=build /out/git-ctx /app/git-ctx
COPY web /var/lib/git-ctx/web
USER 10001
EXPOSE 4747
ENTRYPOINT ["/app/git-ctx"]
