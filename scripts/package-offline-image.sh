#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <version> [image]" >&2
  exit 2
fi

version=${1#v}
image=${2:-git-ctx:v${version}}
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

mkdir -p dist
docker save "$image" | gzip -9 > "$archive"
chmod 0644 "$archive"
(
  cd dist
  sha256sum "git-ctx-v${version}.tar.gz" > "git-ctx-v${version}.tar.gz.sha256"
  chmod 0644 "git-ctx-v${version}.tar.gz.sha256"
  gzip -t "git-ctx-v${version}.tar.gz"
  sha256sum -c "git-ctx-v${version}.tar.gz.sha256"
)

echo "$archive"
echo "$checksum"
