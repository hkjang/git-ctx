#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/gitctx-version-sync.XXXXXX")
trap 'rm -rf "$fixture"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p \
  "$fixture/internal/version" \
  "$fixture/docs" \
  "$fixture/deploy/kubernetes/base"

printf '%s\n' 'const Version = "1.2.3"' > "$fixture/internal/version/version.go"
printf '%s\n' 'info:' '  version: 1.2.3' > "$fixture/docs/openapi.yaml"
printf '%s\n' 'containers:' '    image: git-ctx:v1.2.3' > "$fixture/deploy/kubernetes/base/deployment.yaml"
printf '%s\n' \
  'v0.7.0 이후 오프라인 이미지를 제공합니다.' \
  'VERSION=1.2.3' \
  'git-ctx-v1.2.3.tar.gz' \
  'image git-ctx:v1.2.3' \
  'compatibility image git-ctx:1.2.3' \
  '`--build-arg VERSION=v1.2.3`' \
  'scripts/verify-offline-image.sh 1.2.3 git-ctx-v1.2.3.tar.gz abc' > "$fixture/docs/offline-deployment.md"
printf '%s\n' \
  'scripts/package-offline-image.sh 1.2.3 git-ctx:v1.2.3' \
  'scripts/verify-offline-image.sh 1.2.3 dist/git-ctx-v1.2.3.tar.gz abc' > "$fixture/docs/test-plan.md"
printf '%s\n' '"softwareVersion": "1.2.3",' > "$fixture/docs/index.html"
printf '%s\n' '"softwareVersion": "1.2.3",' > "$fixture/docs/index_en.html"
printf '%s\n' \
  '# git-ctx v1.2.3' \
  'git-ctx-v1.2.3.tar.gz' \
  'git-ctx-v1.2.3.tar.gz.sha256' > "$fixture/docs/release-notes-v1.2.3.md"
printf '%s\n' \
  '2026-08-25 v1.2.3 릴리스 전 검증 결과:' \
  '```text' \
  'Kubernetes Kustomize·v1.2.3 렌더링 PASS' \
  'Docker linux/amd64·v1.2.3 빌드 PASS' \
  '```' \
  '2026-07-30 v1.1.0 릴리스 전 검증 결과:' > "$fixture/docs/completion-audit.md"

sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >/dev/null

# A verification note that mentions an older release is a fact about the
# upgrade path, not a stale version. Requiring every number in the block to
# match the release turned writing that fact into a failed release twice.
printf '%s\n' '이전 0.50 계열 데이터베이스에서 v0.50.0 로 만든 파일을 열어 확인했다.' \
  >> "$fixture/docs/completion-audit.md"
sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >/dev/null

# The artifact lines are a different matter: a stale version there describes an
# image nobody built.
sed -i 's/Docker linux\/amd64·v1.2.3 빌드/Docker linux\/amd64·v1.2.2 빌드/' "$fixture/docs/completion-audit.md"
if sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >"$fixture/out" 2>"$fixture/err"; then
  echo "stale artifact version in the audit block was accepted" >&2
  exit 1
fi
grep -F 'artifact version in newest release audit block is 1.2.2, expected 1.2.3' "$fixture/err" >/dev/null
sed -i 's/Docker linux\/amd64·v1.2.2 빌드/Docker linux\/amd64·v1.2.3 빌드/' "$fixture/docs/completion-audit.md"

sed -i 's/version: 1.2.3/version: 1.2.2/' "$fixture/docs/openapi.yaml"
if sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >"$fixture/out" 2>"$fixture/err"; then
  echo "stale OpenAPI version was accepted" >&2
  exit 1
fi
grep -F 'OpenAPI info.version is 1.2.2, expected 1.2.3' "$fixture/err" >/dev/null
sed -i 's/version: 1.2.2/version: 1.2.3/' "$fixture/docs/openapi.yaml"

printf '%s\n' '  version: 1.2.3' >> "$fixture/docs/openapi.yaml"
if sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >"$fixture/out" 2>"$fixture/err"; then
  echo "duplicate OpenAPI version was accepted" >&2
  exit 1
fi
grep -F 'expected at most 1 OpenAPI info.version value(s) (found 2)' "$fixture/err" >/dev/null
sed -i '$d' "$fixture/docs/openapi.yaml"

sed -i 's/git-ctx-v1.2.3.tar.gz/git-ctx-v1.2.2.tar.gz/' "$fixture/docs/offline-deployment.md"
if sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >"$fixture/out" 2>"$fixture/err"; then
  echo "stale offline archive example was accepted" >&2
  exit 1
fi
grep -F 'offline archive name is 1.2.2, expected 1.2.3' "$fixture/err" >/dev/null

sed -i 's/# git-ctx v1.2.3/# git-ctx v1.2.2/' "$fixture/docs/release-notes-v1.2.3.md"
if sh "$repository_root/scripts/verify-version-sync.sh" "$fixture" >"$fixture/out" 2>"$fixture/err"; then
  echo "stale release-note heading was accepted" >&2
  exit 1
fi
grep -F 'release-note heading is 1.2.2, expected 1.2.3' "$fixture/err" >/dev/null

release_workflow="$repository_root/.github/workflows/release.yml"
grep -F 'echo "notes=release-notes-v${version}.md" >> "$GITHUB_OUTPUT"' "$release_workflow" >/dev/null
grep -F -- '--notes-file "$notes_file"' "$release_workflow" >/dev/null
grep -F -- '--notes-file "$GITHUB_WORKSPACE/release-artifact/$NOTES_NAME"' "$release_workflow" >/dev/null
trusted_checkout=$(awk '
  /- name: Check out trusted release tooling/ && !capture { capture = 1 }
  capture { print }
  capture && /- name: Validate tag and source version/ { exit }
' "$release_workflow")
printf '%s\n' "$trusted_checkout" | grep -F '            .github' >/dev/null

echo "version sync and release-note workflow tests passed"
