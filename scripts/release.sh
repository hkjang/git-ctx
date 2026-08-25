#!/bin/sh
# Cut a release: verify, commit, tag, push — in that order, and stop at the
# first thing that fails.
#
# This exists because doing it by hand went wrong twice in the same way. The
# steps were run as one shell chain, the verification failed in the middle, and
# the tag was pushed anyway — onto the previous commit. The release workflow
# then built the wrong tree, and unpicking it meant deleting a published tag.
# A tag is the one thing that is awkward to take back, so it is created last
# and only from a commit that has already been verified and pushed.
#
# Usage: scripts/release.sh [--message-file FILE] [--remote NAME] [--dry-run]
set -eu

remote=origin
message_file=
dry_run=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --message-file) message_file=${2:?--message-file needs a path}; shift 2 ;;
    --remote) remote=${2:?--remote needs a name}; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) sed -n '1,15p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repository_root"

step() { printf '\n== %s\n' "$1"; }

step "version sync"
sh "$script_dir/verify-version-sync.sh" "$repository_root"

version=$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' internal/version/version.go)
tag="v$version"

if git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null; then
  echo "release: $tag already exists locally; bump the version or delete the tag" >&2
  exit 1
fi
if git ls-remote --exit-code --tags "$remote" "$tag" >/dev/null 2>&1; then
  echo "release: $tag already exists on $remote; a published tag is not replaced" >&2
  exit 1
fi

notes="docs/release-notes-$tag.md"
if [ ! -f "$notes" ]; then
  echo "release: $notes is missing" >&2
  exit 1
fi

step "tests"
go build ./... >/dev/null
go test ./... >/dev/null

if [ -z "$(git status --porcelain)" ]; then
  echo "release: nothing to commit; the working tree is clean" >&2
  exit 1
fi

if [ "$dry_run" -eq 1 ]; then
  step "dry run"
  echo "would commit, tag $tag and push to $remote"
  git status --short
  exit 0
fi

step "commit"
git add -A
if [ -n "$message_file" ]; then
  git commit -q -F "$message_file"
else
  git commit -q -m "release: $tag"
fi

# The commit has to exist and carry the release notes before anything is
# tagged: that is the invariant the two failed releases broke.
if ! git show --stat --name-only HEAD | grep -q "^$notes$"; then
  echo "release: HEAD does not contain $notes; refusing to tag" >&2
  exit 1
fi
committed_version=$(git show "HEAD:internal/version/version.go" | sed -n 's/^const Version = "\([^"]*\)"$/\1/p')
if [ "$committed_version" != "$version" ]; then
  echo "release: HEAD carries version $committed_version, not $version; refusing to tag" >&2
  exit 1
fi

step "push commit"
git push "$remote" HEAD

step "tag"
git tag -a "$tag" -m "git-ctx $tag"
git push "$remote" "$tag"

printf '\nreleased %s\n' "$tag"
