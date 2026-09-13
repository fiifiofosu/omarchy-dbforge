#!/usr/bin/env bash
# Removes the DBForge widget from the Omarchy shell.
#
# This touches the widget only. DBForge itself, its daemon and every database
# instance are left exactly as they were -- see the repository README for
# uninstalling those.
set -euo pipefail

PLUGIN_ID="io.github.fiifiofosu.dbforge"
DEST="${OMARCHY_PLUGIN_DIR:-$HOME/.config/omarchy/plugins}/$PLUGIN_ID"

say()  { printf '\033[32m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }

if command -v omarchy >/dev/null 2>&1; then
  omarchy plugin remove "$PLUGIN_ID" --yes >/dev/null 2>&1 ||
    warn "omarchy could not remove the plugin; removing the folder directly"
fi

if [ -d "$DEST" ]; then
  rm -rf "$DEST"
  say "Removed $DEST"
else
  say "Nothing to remove at $DEST"
fi

omarchy-shell shell rescanPlugins >/dev/null 2>&1 || true
say "Done."
