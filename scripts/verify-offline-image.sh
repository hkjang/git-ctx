#!/bin/sh
set -eu

export LC_ALL=C

if [ "$#" -lt 1 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 <version> [archive] [expected-commit]" >&2
  exit 2
fi

version=${1#v}
archive=${2:-dist/git-ctx-v${version}.tar.gz}
expected_commit=${3:-}
checksum="${archive}.sha256"
canonical="git-ctx:v${version}"
compatible="git-ctx:${version}"

if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$'; then
  echo "invalid semantic version: $version" >&2
  exit 2
fi
if [ ! -s "$archive" ] || [ ! -s "$checksum" ]; then
  echo "offline image archive or checksum is missing: $archive" >&2
  exit 1
fi

gzip -t "$archive"
archive_dir=$(dirname "$archive")
archive_name=$(basename "$archive")
if [ "$(wc -l < "$checksum" | tr -d ' ')" -ne 1 ]; then
  echo "checksum file must contain exactly one entry" >&2
  exit 1
fi
checksum_entry=$(sed -n '1p' "$checksum")
checksum_hash=${checksum_entry%% *}
checksum_file=${checksum_entry#*  }
if ! printf '%s\n' "$checksum_hash" | grep -Eq '^[0-9a-f]{64}$' || [ "$checksum_file" != "$archive_name" ]; then
  echo "checksum file must name exactly $archive_name" >&2
  exit 1
fi
(
  cd "$archive_dir"
  sha256sum -c "${archive_name}.sha256"
)

# docker load가 성공해야 gzip 내부가 실제 Docker archive라는 사실까지 검증된다.
# 이후 검사는 archive가 복원한 두 태그를 대상으로 수행한다.
load_output=$(docker load --input "$archive")
if [ "$(printf '%s\n' "$load_output" | grep -Fxc "Loaded image: $canonical" || true)" -ne 1 ] ||
   [ "$(printf '%s\n' "$load_output" | grep -Fxc "Loaded image: $compatible" || true)" -ne 1 ] ||
   [ "$(printf '%s\n' "$load_output" | grep -c '^Loaded image:' || true)" -ne 2 ]; then
  echo "archive must contain exactly $canonical and $compatible" >&2
  printf '%s\n' "$load_output" >&2
  exit 1
fi
canonical_id=$(docker image inspect "$canonical" --format '{{.Id}}')
compatible_id=$(docker image inspect "$compatible" --format '{{.Id}}')
if [ "$canonical_id" != "$compatible_id" ]; then
  echo "archive image aliases point to different images" >&2
  exit 1
fi

platform=$(docker image inspect "$canonical" --format '{{.Os}}/{{.Architecture}}')
if [ "$platform" != "linux/amd64" ]; then
  echo "expected linux/amd64 image, got $platform" >&2
  exit 1
fi

reported=$(docker run --rm --entrypoint /app/git-ctx "$canonical" -version 2>/dev/null | head -1 || true)
reported_version=${reported%% *}
if [ "$reported_version" != "$version" ]; then
  echo "archive image reports $reported, expected $version" >&2
  exit 1
fi

label_version=$(docker image inspect "$canonical" --format '{{index .Config.Labels "org.opencontainers.image.version"}}')
if [ "${label_version#v}" != "$version" ]; then
  echo "archive OCI version label is $label_version, expected v$version" >&2
  exit 1
fi
label_commit=$(docker image inspect "$canonical" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
if [ -n "$expected_commit" ] && [ "$label_commit" != "$expected_commit" ]; then
  echo "archive OCI revision is $label_commit, expected $expected_commit" >&2
  exit 1
fi
label_created=$(docker image inspect "$canonical" --format '{{index .Config.Labels "org.opencontainers.image.created"}}')
if [ -z "$label_created" ]; then
  echo "archive OCI created label is empty" >&2
  exit 1
fi

image_user=$(docker image inspect "$canonical" --format '{{.Config.User}}')
if [ "$image_user" != "10001" ]; then
  echo "archive image runs as unexpected user $image_user" >&2
  exit 1
fi

echo "verified $archive ($platform, $canonical_id, revision $label_commit)"
