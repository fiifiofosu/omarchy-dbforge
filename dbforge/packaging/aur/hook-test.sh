#!/usr/bin/env bash
# Exercises the pacman install hooks without touching the system.
#
# The hooks are the part of packaging that only runs on a real install, on
# someone else's machine, once -- so they are the part most likely to be wrong
# and least likely to be noticed. This sources them with a stub `systemctl` on
# PATH and asserts on the calls they make.
set -euo pipefail

HOOK="$(dirname "$0")/dbforge-git/dbforge.install"
BIN="$(mktemp -d)"
trap 'rm -rf "$BIN"' EXIT

printf '#!/bin/sh\necho "$*" >> "$LOG"\n' > "$BIN/systemctl"
chmod +x "$BIN/systemctl"
export PATH="$BIN:$PATH"

fail() { printf '\033[31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
pass() { printf '\033[32mok\033[0m   %s\n' "$*"; }

# run <hook-function> <marker-path> -> stdout of the hook, calls in $CALLS
run() {
  LOG="$(mktemp)"; export LOG
  _DBFORGE_MARKER="$2"; export _DBFORGE_MARKER
  ( . "$HOOK"; "$1" ) > /dev/null
  CALLS="$(cat "$LOG")"
}

run post_install "$BIN/absent"
[ "$CALLS" = "--global enable dbforged.service" ] \
  || fail "post_install should enable the unit, got: [$CALLS]"
pass "post_install enables dbforged"

touch "$BIN/marker"
run post_install "$BIN/marker"
[ -z "$CALLS" ] || fail "the opt-out marker was ignored, got: [$CALLS]"
pass "post_install honours the opt-out marker"

# The marker has to keep working on upgrades, or the opt-out silently lapses
# the first time the package is updated.
run post_upgrade "$BIN/marker"
[ -z "$CALLS" ] || fail "post_upgrade re-enabled despite the marker: [$CALLS]"
pass "post_upgrade honours the opt-out marker"

run post_upgrade "$BIN/absent"
[ "$CALLS" = "--global enable dbforged.service" ] \
  || fail "post_upgrade should keep the unit enabled, got: [$CALLS]"
pass "post_upgrade keeps dbforged enabled"

run pre_remove "$BIN/absent"
[ "$CALLS" = "--global disable dbforged.service" ] \
  || fail "pre_remove should disable the unit, got: [$CALLS]"
pass "pre_remove disables dbforged"

# Removal must tell the user their databases are still there. Losing this
# message is how someone concludes the data is gone and deletes it.
out="$( . "$HOOK"; post_remove )"
for needle in 'local/share/dbforge' 'io.dbforge.managed' 'wipe-data'; do
  grep -q "$needle" <<<"$out" || fail "post_remove no longer mentions $needle"
done
pass "post_remove explains where the databases still are"
