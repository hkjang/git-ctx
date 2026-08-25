#!/bin/sh
set -u

export LC_ALL=C

if [ "$#" -gt 1 ]; then
  echo "usage: $0 [repository-root]" >&2
  exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=${1:-$(CDPATH= cd -- "$script_dir/.." && pwd)}
failures=0

fail() {
  echo "version sync: $*" >&2
  failures=$((failures + 1))
}

require_file() {
  if [ ! -f "$repository_root/$1" ]; then
    fail "required file is missing: $1"
    return 1
  fi
  return 0
}

# Check every non-empty value produced by a targeted extractor. min_count keeps
# an accidentally removed release example from looking like a successful sync;
# max_count=1 is used for metadata that must have exactly one source of truth.
check_values() {
  file=$1
  label=$2
  values=$3
  min_count=$4
  max_count=${5:-0}
  count=$(printf '%s\n' "$values" | awk 'NF { count++ } END { print count + 0 }')
  if [ "$count" -lt "$min_count" ]; then
    fail "$file: expected $label (found $count)"
    return
  fi
  if [ "$max_count" -gt 0 ] && [ "$count" -gt "$max_count" ]; then
    fail "$file: expected at most $max_count $label value(s) (found $count)"
    return
  fi
  printf '%s\n' "$values" | while IFS= read -r found; do
    [ -z "$found" ] && continue
    if [ "$found" != "$version" ]; then
      echo "version sync: $file: $label is $found, expected $version" >&2
      exit 1
    fi
  done || failures=$((failures + 1))
}

for required in \
  internal/version/version.go \
  docs/openapi.yaml \
  deploy/kubernetes/base/deployment.yaml \
  docs/offline-deployment.md \
  docs/test-plan.md \
  docs/index.html \
  docs/index_en.html \
  docs/completion-audit.md; do
  require_file "$required" || :
done

if [ "$failures" -ne 0 ]; then
  exit 1
fi

version_values=$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' "$repository_root/internal/version/version.go")
version_count=$(printf '%s\n' "$version_values" | awk 'NF { count++ } END { print count + 0 }')
if [ "$version_count" -ne 1 ]; then
  fail "internal/version/version.go: expected exactly one Version constant"
  version=
else
  version=$version_values
fi
if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$'; then
  fail "internal/version/version.go: Version is not a supported semantic version: $version"
fi

if [ -n "$version" ]; then
  release_notes="docs/release-notes-v$version.md"
  if require_file "$release_notes"; then
    check_values "$release_notes" "release-note heading" \
      "$(sed -n 's/^# git-ctx v\([0-9][0-9A-Za-z.-]*\)$/\1/p' "$repository_root/$release_notes")" 1 1
    check_values "$release_notes" "offline archive name" \
      "$(sed -n 's/.*git-ctx-v\([0-9][0-9A-Za-z.-]*\)\.tar\.gz.*/\1/p' "$repository_root/$release_notes")" 1
  fi
  check_values "docs/openapi.yaml" "OpenAPI info.version" \
    "$(sed -n 's/^  version: \([^ ]*\)$/\1/p' "$repository_root/docs/openapi.yaml")" 1 1
  check_values "deploy/kubernetes/base/deployment.yaml" "git-ctx image tag" \
    "$(sed -n 's/^[[:space:]]*image: git-ctx:v\([^[:space:]]*\)$/\1/p' "$repository_root/deploy/kubernetes/base/deployment.yaml")" 1 1

  # These two pages publish structured software metadata to search engines.
  for page in docs/index.html docs/index_en.html; do
    check_values "$page" "softwareVersion" \
      "$(sed -n 's/.*"softwareVersion": "\([^"]*\)".*/\1/p' "$repository_root/$page")" 1 1
  done

  # Only current deployment examples are checked. The introductory v0.7.0
  # compatibility history and other deliberately historical prose are ignored.
  offline="$repository_root/docs/offline-deployment.md"
  check_values "docs/offline-deployment.md" "VERSION example" \
    "$(sed -n 's/^VERSION=\([^[:space:]]*\)$/\1/p' "$offline")" 1
  check_values "docs/offline-deployment.md" "offline archive name" \
    "$(sed -n 's/.*git-ctx-v\([0-9][0-9A-Za-z.-]*\)\.tar\.gz.*/\1/p' "$offline")" 1
  check_values "docs/offline-deployment.md" "literal image tag" \
    "$(sed -n 's/.*git-ctx:v\([0-9][0-9A-Za-z.-]*\).*/\1/p' "$offline")" 1
  check_values "docs/offline-deployment.md" "compatibility image tag" \
    "$(sed -n 's/.*git-ctx:\([0-9][0-9A-Za-z.-]*\).*/\1/p' "$offline")" 1
  check_values "docs/offline-deployment.md" "Docker VERSION build argument" \
    "$(sed -n 's/.*--build-arg VERSION=v\([0-9][0-9A-Za-z.-]*\).*/\1/p' "$offline")" 1
  check_values "docs/offline-deployment.md" "verification script version" \
    "$(sed -n 's/.*verify-offline-image\.sh \([^[:space:]]*\).*/\1/p' "$offline")" 1

  plan="$repository_root/docs/test-plan.md"
  check_values "docs/test-plan.md" "package script version" \
    "$(sed -n 's/.*package-offline-image\.sh \([^[:space:]]*\).*/\1/p' "$plan")" 1
  check_values "docs/test-plan.md" "verification script version" \
    "$(sed -n 's/.*verify-offline-image\.sh \([^[:space:]]*\).*/\1/p' "$plan")" 1
  check_values "docs/test-plan.md" "offline archive name" \
    "$(sed -n 's/.*git-ctx-v\([0-9][0-9A-Za-z.-]*\)\.tar\.gz.*/\1/p' "$plan")" 1

  # completion-audit.md is an append-only history. Only its newest release
  # verification heading represents the current release; older headings stay.
  newest_audit_version=$(sed -n 's/^.* v\([0-9][0-9A-Za-z.-]*\) 릴리스 전 검증 결과:$/\1/p' \
    "$repository_root/docs/completion-audit.md" | sed -n '1p')
  check_values "docs/completion-audit.md" "newest release audit heading" \
    "$newest_audit_version" 1 1
  newest_audit_block=$(awk '
    !release && / v[0-9][0-9A-Za-z.-]* 릴리스 전 검증 결과:$/ { release = 1; next }
    release && !block && $0 == "```text" { block = 1; next }
    block && $0 == "```" { exit }
    block { print }
  ' "$repository_root/docs/completion-audit.md")
  # The block records what was verified, and its Kustomize and Docker lines
  # name the artifacts this release produces. Those lines must carry this
  # version; the rest of the block is prose, and a note that mentions an older
  # release — "upgraded from 0.50" — is a fact, not a stale version. Checking
  # every number in the block made writing that fact break the release.
  check_values "docs/completion-audit.md" "artifact version in newest release audit block" \
    "$(printf '%s\n' "$newest_audit_block" | grep -E 'Kustomize|Docker' | sed -n 's/.*v\([0-9][0-9A-Za-z.-]*\).*/\1/p')" 2
fi

if [ "$failures" -ne 0 ]; then
  exit 1
fi

echo "version sync verified: v$version"
