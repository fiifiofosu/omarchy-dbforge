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

printf '#!/bin/sh\necho "systemctl $*" >> "$LOG"\n' > "$BIN/systemctl"
# loginctl is asked two things: whether lingering is on, and to turn it on.
# The stub reports off, so the enable path is the one under test.
cat > "$BIN/loginctl" <<'STUB'
#!/bin/sh
case "$1" in
  show-user) echo no ;;
  *) echo "loginctl $*" >> "$LOG" ;;
esac
STUB
# The hooks need to work out who pacman was run for.
printf '#!/bin/sh\necho dv\n' > "$BIN/logname"
# ...and check that user exists. Stubbed rather than using a real account, so
# the test asserts the hook's logic and not the machine's user list: a
# hardcoded name that happens to exist on a developer's box does not exist on
# a CI runner, and the hook then bails out before doing anything.
cat > "$BIN/id" <<'STUB'
#!/bin/sh
# The hook calls `id -u <user>` purely as an existence check.
case "$2" in dv) exit 0 ;; *) exit 1 ;; esac
STUB
chmod +x "$BIN/systemctl" "$BIN/loginctl" "$BIN/logname" "$BIN/id"
export PATH="$BIN:$PATH"
export SUDO_USER=dv

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
grep -q -- '--global enable dbforged.service' <<<"$CALLS" \
  || fail "post_install should enable the unit, got: [$CALLS]"
pass "post_install enables dbforged"

# The whole point of doing this in the hook: an install that ends with
# homework is not an install. Lingering needs root, and the hook has it.
grep -q 'loginctl enable-linger dv' <<<"$CALLS" \
  || fail "post_install should enable lingering, got: [$CALLS]"
pass "post_install enables lingering for the invoking user"

# ...and starts the service in that user's session, so the install is usable
# now rather than after a logout.
grep -q -- '--user start dbforged.service' <<<"$CALLS" \
  || fail "post_install should start the service for the user, got: [$CALLS]"
pass "post_install starts dbforged in the user's session"

# Root is not a user to set up. A package that enabled lingering for root
# would be both useless and rude.
SUDO_USER=root run post_install "$BIN/absent"
grep -q 'enable-linger root' <<<"$CALLS" \
  && fail "the hook tried to set up root: [$CALLS]"
pass "post_install never sets root up"

# A user pacman names but who does not exist is not a user to set up either.
SUDO_USER=ghost run post_install "$BIN/absent"
grep -q 'enable-linger' <<<"$CALLS" \
  && fail "the hook set up a user that does not exist: [$CALLS]"
pass "post_install skips a user that does not exist"
SUDO_USER=dv

touch "$BIN/marker"
run post_install "$BIN/marker"
[ -z "$CALLS" ] || fail "the opt-out marker was ignored, got: [$CALLS]"
pass "post_install honours the opt-out marker (nothing enabled, no lingering)"

# The marker has to keep working on upgrades, or the opt-out silently lapses
# the first time the package is updated.
run post_upgrade "$BIN/marker"
[ -z "$CALLS" ] || fail "post_upgrade re-enabled despite the marker: [$CALLS]"
pass "post_upgrade honours the opt-out marker"

run post_upgrade "$BIN/absent"
grep -q -- '--global enable dbforged.service' <<<"$CALLS" \
  || fail "post_upgrade should keep the unit enabled, got: [$CALLS]"
pass "post_upgrade keeps dbforged enabled"

# The new binaries are on disk but the running daemon is the old one.
# Restarting it does not stop the databases, so the hook does it rather than
# printing an instruction.
grep -q -- 'restart dbforged.service' <<<"$CALLS" \
  || fail "post_upgrade should restart the daemon, got: [$CALLS]"
pass "post_upgrade restarts the daemon itself"

run pre_remove "$BIN/absent"
[ "$CALLS" = "systemctl --global disable dbforged.service" ] \
  || fail "pre_remove should disable the unit, got: [$CALLS]"
pass "pre_remove disables dbforged"

# Removal must tell the user their databases are still there. Losing this
# message is how someone concludes the data is gone and deletes it.
out="$( . "$HOOK"; post_remove )"
for needle in 'local/share/dbforge' 'io.dbforge.managed' 'wipe-data'; do
  grep -q "$needle" <<<"$out" || fail "post_remove no longer mentions $needle"
done
pass "post_remove explains where the databases still are"
