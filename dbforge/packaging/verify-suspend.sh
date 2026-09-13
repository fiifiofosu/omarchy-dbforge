#!/usr/bin/env bash
# Verifies that DBForge instances survive a suspend/resume cycle.
#
# This cannot be automated in CI, and it is the one Phase 2 behaviour that
# genuinely needs a physical lid close. Run it in two halves:
#
#   ./verify-suspend.sh before    # writes a marker, records state
#   <suspend the machine, resume it>
#   ./verify-suspend.sh after     # checks everything came back
set -euo pipefail

STATE="${XDG_RUNTIME_DIR:-/tmp}/dbforge-suspend-check"
ID="suspend-check"
MARKER="survived-suspend"

die() { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
ok()  { printf '\033[32mok: %s\033[0m\n' "$*"; }
info(){ printf '%s\n' "$*"; }

need() { command -v "$1" >/dev/null || die "$1 not found"; }
need dbctl
need podman

case "${1:-}" in
before)
  dbctl rm "$ID" --wipe-data --force --yes >/dev/null 2>&1 || true
  info "Creating $ID ..."
  dbctl create redis:7 --name "$ID" >/dev/null

  port="$(dbctl list --json | grep -A6 "\"id\": \"$ID\"" | sed -n 's/.*"port": \([0-9]*\).*/\1/p' | head -1)"
  [ -n "$port" ] || die "could not determine the port for $ID"

  # Write through the published port, then force a save so the value is on
  # disk rather than only in memory.
  printf 'SET %s %s\r\nSAVE\r\n' "$MARKER" "yes" | timeout 10 \
    podman run --rm -i --network=host docker.io/library/redis:7 \
    redis-cli -h 127.0.0.1 -p "$port" >/dev/null

  printf '%s\n' "$port" > "$STATE"
  ok "wrote marker on port $port"
  info ""
  info "Now suspend the machine (close the lid, or: systemctl suspend),"
  info "resume it, and run:  $0 after"
  ;;

after)
  [ -f "$STATE" ] || die "no saved state; run '$0 before' first"
  port="$(cat "$STATE")"

  info "Checking after resume (port $port) ..."

  systemctl --user is-active dbforged >/dev/null || die "dbforged is not running after resume"
  ok "dbforged is running"

  dbctl list >/dev/null || die "dbctl cannot reach the daemon after resume"
  ok "daemon is reachable (its podman connection survived)"

  status="$(dbctl list | awk -v id="$ID" '$1==id {print $5}')"
  [ "$status" = "running" ] || die "instance status is '$status', expected running"
  ok "instance is running"

  got="$(timeout 10 podman run --rm --network=host docker.io/library/redis:7 \
    redis-cli -h 127.0.0.1 -p "$port" GET "$MARKER" 2>/dev/null || true)"
  [ "$got" = "yes" ] || die "data did not survive: GET returned '$got'"
  ok "data survived the suspend/resume cycle"

  info ""
  ok "ALL CHECKS PASSED"
  info "Clean up with: dbctl rm $ID --wipe-data --force"
  rm -f "$STATE"
  ;;

*)
  echo "usage: $0 before|after" >&2
  exit 2
  ;;
esac
