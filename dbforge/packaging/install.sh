#!/usr/bin/env bash
# Installs DBForge for the current user. Deliberately not run by `make install`
# without consent: it enables a service and turns on user lingering.
set -euo pipefail

PREFIX="${PREFIX:-$HOME/.local}"
UNIT_DIR="$HOME/.config/systemd/user"

say() { printf '\033[32m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }

if ! command -v podman >/dev/null 2>&1; then
  warn "podman is not installed. DBForge cannot run without it."
  exit 1
fi

say "Enabling the rootless podman socket"
systemctl --user enable --now podman.socket

say "Installing the dbforged unit"
mkdir -p "$UNIT_DIR"
# The unit is a template: ExecStart and PATH depend on where the binaries went.
sed -e "s|@BINDIR@|$PREFIX/bin|g" \
    -e "s|@PATH@|$PREFIX/bin:/usr/local/bin:/usr/bin|g" \
    "$(dirname "$0")/dbforged.service.in" > "$UNIT_DIR/dbforged.service"
chmod 644 "$UNIT_DIR/dbforged.service"
systemctl --user daemon-reload
systemctl --user enable --now dbforged

# Without lingering, the systemd user manager is torn down at logout, taking
# the daemon and every database with it -- and nothing starts at boot until
# someone logs in. This is the single step that makes "survives login/logout"
# actually true.
if [ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no)" != "yes" ]; then
  warn ""
  warn "User lingering is off. Without it your databases stop at logout and do"
  warn "not come back until you log in again. Enable it with:"
  warn ""
  warn "    sudo loginctl enable-linger $USER"
  warn ""
else
  say "User lingering is already enabled"
fi

say "Done. Try: dbctl create postgres:16 --name app-db"
