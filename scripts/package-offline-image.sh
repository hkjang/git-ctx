#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <version> [image]" >&2
  exit 2
fi

version=${1#v}
image=${2:-git-ctx:v${version}}
canonical="git-ctx:v${version}"
compatible="git-ctx:${version}"
archive="dist/git-ctx-v${version}.tar.gz"
checksum="${archive}.sha256"

case "$version" in
  *[!0-9A-Za-z.-]*|'') echo "invalid version: $version" >&2; exit 2 ;;
esac

if [ -e "$archive" ] || [ -e "$checksum" ]; then
  echo "release artifact already exists: $archive" >&2
  exit 1
fi

platform=$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')
if [ "$platform" != "linux/amd64" ]; then
  echo "expected linux/amd64 image, got $platform" >&2
  exit 1
fi

# 이미지가 실제로 그 버전을 보고하는지 확인합니다. 태그만 새로 붙인 예전 이미지를
# 릴리스 아티팩트로 내보내는 사고를 여기서 잡습니다.
reported=$(docker run --rm --entrypoint /app/git-ctx "$image" -version 2>/dev/null | head -1 || true)
case "$reported" in
  *"$version"*) : ;;
  '') echo "이미지에서 버전을 확인하지 못했습니다" >&2; exit 1 ;;
  *) echo "이미지가 보고한 버전($reported)이 요청한 버전($version)과 다릅니다" >&2; exit 1 ;;
esac

mkdir -p dist
# 문서와 과거 배포 구성에서 사용된 두 태그를 모두 보존합니다. 동일 이미지에 붙인
# 별칭이므로 layer 용량은 중복되지 않습니다.
docker tag "$image" "$canonical"
docker tag "$image" "$compatible"
docker save "$canonical" "$compatible" | gzip -9 > "$archive"
chmod 0644 "$archive"
(
  cd dist
  sha256sum "git-ctx-v${version}.tar.gz" > "git-ctx-v${version}.tar.gz.sha256"
  chmod 0644 "git-ctx-v${version}.tar.gz.sha256"
  gzip -t "git-ctx-v${version}.tar.gz"
  sha256sum -c "git-ctx-v${version}.tar.gz.sha256"
)

canonical_id=$(docker image inspect "$canonical" --format '{{.Id}}')
compatible_id=$(docker image inspect "$compatible" --format '{{.Id}}')
if [ "$canonical_id" != "$compatible_id" ]; then
  echo "릴리스 이미지 별칭이 서로 다른 이미지를 가리킵니다" >&2
  exit 1
fi

echo "$archive"
echo "$checksum"
