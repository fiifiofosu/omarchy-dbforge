#!/usr/bin/env bash
# Installs DBForge for the current user and leaves nothing for you to do.
#
# Every step here is one the tool needs in order to work as described. Printing
# them as instructions instead would just be asking you to run an installer by
# hand, so this does them -- and says what it did, so nothing happens to your
# machine that you cannot see.
#
# Usage: packaging/install.sh [--no-linger] [--yes]
set -euo pipefail

PREFIX="${PREFIX:-$HOME/.local}"
UNIT_DIR="$HOME/.config/systemd/user"
WANT_LINGER=1
ASSUME_YES=0

for arg in "$@"; do
  case "$arg" in
    --no-linger) WANT_LINGER=0 ;;
    --yes|-y)    ASSUME_YES=1 ;;
    -h|--help)
      sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) printf 'unknown option: %s\n' "$arg" >&2; exit 2 ;;
  esac
done

say()  { printf '\033[32m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

command -v podman >/dev/null 2>&1 ||
  die "podman is not installed. DBForge cannot run without it: sudo pacman -S podman"

say "Installing the dbforged unit"
mkdir -p "$UNIT_DIR"
# The unit is a template: ExecStart and PATH depend on where the binaries went.
sed -e "s|@BINDIR@|$PREFIX/bin|g" \
    -e "s|@PATH@|$PREFIX/bin:/usr/local/bin:/usr/bin|g" \
    "$(dirname "$0")/dbforged.service.in" > "$UNIT_DIR/dbforged.service"
chmod 644 "$UNIT_DIR/dbforged.service"
systemctl --user daemon-reload

# podman.socket is not enabled separately: dbforged.service Wants= it, so
# starting the daemon starts the socket, and enabling the daemon brings both up
# at login. Enabling it here as well would only add a second thing to keep in
# step with the first.
say "Enabling and starting dbforged"
systemctl --user enable --now dbforged

# Without lingering, the systemd user manager is torn down at logout, taking
# the daemon and every database with it -- and nothing starts at boot until
# someone logs in. This is the single step that makes "your databases are there
# tomorrow" true, and it is the only one that needs root.
linger_state() { loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no; }

if [ "$WANT_LINGER" -eq 0 ]; then
  warn "Skipping user lingering (--no-linger)."
  warn "Your databases will stop when you log out."
elif [ "$(linger_state)" = "yes" ]; then
  say "User lingering already enabled"
else
  say "Enabling user lingering so your databases survive logout"
  say "  this needs root: sudo loginctl enable-linger $USER"
  if [ "$ASSUME_YES" -eq 0 ] && [ -t 0 ]; then
    read -r -p "  proceed? [Y/n] " reply
    case "$reply" in [nN]*) WANT_LINGER=0 ;; esac
  fi
  if [ "$WANT_LINGER" -eq 1 ]; then
    if sudo -n true 2>/dev/null || [ -t 0 ]; then
      if sudo loginctl enable-linger "$USER"; then
        say "  enabled"
      else
        warn "  could not enable lingering; run it yourself when convenient:"
        warn "      sudo loginctl enable-linger $USER"
      fi
    else
      # Non-interactive with no cached sudo: refusing to hang on a password
      # prompt nobody can answer is better than appearing to freeze.
      warn "  no terminal for the sudo prompt. Run this when convenient:"
      warn "      sudo loginctl enable-linger $USER"
    fi
  else
    warn "  skipped. Run it later with: sudo loginctl enable-linger $USER"
  fi
fi

echo
say "Done. Nothing else to run."
say "  dbctl create postgres:16 --name app-db"
say "  dbctl doctor     # if anything looks wrong"
