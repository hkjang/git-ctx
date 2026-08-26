#!/bin/sh
# The same database, opened by both binaries this project builds.
#
# SQLite exposes FTS5 only when the driver was built with it, and the search
# index is maintained by triggers that need it. Triggers live in the schema, so
# they outlive the binary that created them: a build without the module inherits
# triggers it cannot run, and a trigger that cannot run fails the statement that
# fired it. That is every write to document_chunks — indexing stops completely,
# and the error names an SQLite module rather than anything an operator did.
#
# `go build ./...` is the command without the tag, so this is one forgotten flag
# away rather than hypothetical.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
cd "$root"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
database=$work/crossover.db

fail() { printf 'build-mode: %s\n' "$1" >&2; exit 1; }

printf '== building both binaries\n'
go build -tags sqlite_fts5 -o "$work/with-module" ./test/store/probe
go build -o "$work/without-module" ./test/store/probe

printf '== a build with the module indexes and searches\n'
with=$("$work/with-module" "$database" first) || fail "the build with the module could not index: $with"
printf '%s\n' "$with" | sed 's/^/   /'
case $with in
  *"fulltext=true"*) ;;
  *) printf '   no FTS5 in this toolchain; there is no crossover to test\n'; exit 0 ;;
esac
case $with in
  *"search=1"*) ;;
  *) fail "the index did not answer for content it holds" ;;
esac

printf '== the same database, opened by a build without the module\n'
without=$("$work/without-module" "$database" second) || fail "indexing stopped for a build without the module: $without"
printf '%s\n' "$without" | sed 's/^/   /'
case $without in
  *"fulltext=false"*) ;;
  *) fail "the store reported a full-text index this build cannot query" ;;
esac
for step in insert update stamp; do
  case $without in
    *"$step=ok"*) ;;
    *) fail "$step failed for a build without the module" ;;
  esac
done

printf '== back to the module: the index it left behind is rebuilt, not trusted\n'
again=$("$work/with-module" "$database" third) || fail "reopening with the module failed: $again"
printf '%s\n' "$again" | sed 's/^/   /'
case $again in
  *"fulltext=true"*) ;;
  *) fail "the index was given up rather than rebuilt" ;;
esac
# Three chunks now carry the same text; all three have to be findable, including
# the one written while the index was unmaintained.
case $again in
  *"search=3"*) ;;
  *) fail "the rebuilt index does not describe the table: $again" ;;
esac

printf '\nbuild-mode: both binaries share the database\n'
