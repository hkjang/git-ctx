#!/bin/sh
# A database written by a released binary, opened by this one.
#
# An on-premises product's riskiest moment is the upgrade, and the only tests
# for it built a "legacy schema" by hand — a guess at what an old version left
# behind, written by the same people who wrote the migration. This takes the
# actual binary from a released tag, lets it create and fill a database, and
# then opens that database with the working tree.
#
# The probe both halves run uses only what every one of these versions has:
# store.Open, Rebind, and the tables from the first migration. Anything newer
# would not compile against the old tree.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
cd "$root"

# The versions an installation might plausibly still be running. Each is built
# from its own worktree, so nothing here depends on the current tree.
tags=${GIT_CTX_UPGRADE_FROM:-"v0.60.0 v0.63.0 v0.65.0"}

work=$(mktemp -d)
cleanup() {
  for tag in $tags; do
    git worktree remove --force "$work/src-$tag" >/dev/null 2>&1 || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

fail() { printf 'upgrade: %s\n' "$1" >&2; exit 1; }

printf '== building this tree\n'
go build -tags sqlite_fts5 -o "$work/current" ./test/store/upgradeprobe
go build -tags sqlite_fts5 -o "$work/server" ./cmd/git-ctx

# The server takes its port from a stored setting, so it always comes up on
# 4747. Anything already listening there belongs to somebody else.
if curl -sS -o /dev/null --max-time 1 http://127.0.0.1:4747/ 2>/dev/null; then
  printf '   something is already listening on 4747; the server half is skipped\n'
  serve=0
else
  serve=1
fi

for tag in $tags; do
  printf '\n== from %s\n' "$tag"
  if ! git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null; then
    printf '   %s is not in this clone; skipped\n' "$tag"
    continue
  fi
  git worktree add -q --detach "$work/src-$tag" "$tag"
  mkdir -p "$work/src-$tag/test/store/upgradeprobe"
  cp test/store/upgradeprobe/main.go "$work/src-$tag/test/store/upgradeprobe/main.go"
  ( cd "$work/src-$tag" && go build -tags sqlite_fts5 -o "$work/old-$tag" ./test/store/upgradeprobe ) \
    || fail "$tag could not build the probe; the probe uses an API that version does not have"

  database=$work/upgrade-$tag.db
  seeded=$("$work/old-$tag" seed "$database") || fail "$tag could not write its own database: $seeded"
  printf '%s\n' "$seeded" | sed 's/^/   old: /'

  upgraded=$("$work/current" verify "$database") || fail "this tree could not open a $tag database: $upgraded"
  printf '%s\n' "$upgraded" | sed 's/^/   new: /'

  case $upgraded in
    *"chunks=2"*) ;;
    *) fail "the rows written by $tag did not survive the upgrade: $upgraded" ;;
  esac
  case $upgraded in
    *"settleInvoice"*) ;;
    *) fail "content written by $tag came back changed: $upgraded" ;;
  esac
  case $upgraded in
    *"write=ok"*) ;;
    *) fail "the database is not writable after upgrading from $tag: $upgraded" ;;
  esac

  # The migration count must not go backwards, and the upgrade must actually
  # apply the migrations added since that tag.
  old_migrations=$(printf '%s\n' "$seeded" | sed -n 's/^migrations=\([0-9]*\)$/\1/p')
  new_migrations=$(printf '%s\n' "$upgraded" | sed -n 's/^migrations=\([0-9]*\)$/\1/p')
  [ -n "$old_migrations" ] && [ -n "$new_migrations" ] || fail "the migration count could not be read for $tag"
  [ "$new_migrations" -ge "$old_migrations" ] \
    || fail "opening a $tag database removed migrations: $old_migrations -> $new_migrations"
  printf '   migrations: %s -> %s\n' "$old_migrations" "$new_migrations"

  # Opening the database is the smaller half. The server reads its settings out
  # of it, decrypts them with the configured key, brings up the worker and
  # serves the console — none of which store.Open touches.
  [ "$serve" -eq 1 ] || continue
  GIT_CTX_DB_DSN="file:$database?_foreign_keys=on&_busy_timeout=5000" \
    GIT_CTX_RECOVERY_KEY="$(head -c 48 /dev/urandom | base64 | tr -d '\n')" \
    "$work/server" >"$work/server-$tag.log" 2>&1 &
  server_pid=$!
  up=0
  for _ in $(seq 1 60); do
    if curl -sS -o "$work/status-$tag.json" --max-time 2 http://127.0.0.1:4747/api/v1/public/status 2>/dev/null; then
      up=1
      break
    fi
    sleep 0.5
  done
  kill "$server_pid" >/dev/null 2>&1 || true
  wait "$server_pid" >/dev/null 2>&1 || true
  [ "$up" -eq 1 ] || fail "the server did not come up on a $tag database: $(tail -5 "$work/server-$tag.log")"
  grep -q '"driver":"sqlite"' "$work/status-$tag.json" \
    || fail "the status endpoint did not answer on a $tag database: $(cat "$work/status-$tag.json")"
  printf '   server: started and answered on the upgraded database\n'
done

printf '\nupgrade: every released database opened, read and written\n'
