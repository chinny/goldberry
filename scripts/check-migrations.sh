#!/usr/bin/env sh
# Fails when migrations/sqlite and migrations/postgres don't hold the same
# numbered files (plan §9.1: the two trees are kept in lockstep).
set -eu
cd "$(dirname "$0")/.."
list() { (cd "migrations/$1" && ls -1 *.sql | sort); }
a=$(list sqlite); b=$(list postgres)
if [ "$a" != "$b" ]; then
  echo "migration trees differ:" >&2
  { echo "sqlite:"; echo "$a"; echo "postgres:"; echo "$b"; } >&2
  exit 1
fi
echo "migrations in lockstep: $(echo "$a" | wc -l | tr -d ' ') file(s)"
