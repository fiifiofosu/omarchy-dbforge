#!/usr/bin/env bash
# Adds the DBForge module to an existing waybar config.
#
# Editing someone's bar config is intrusive, so this backs up first, refuses to
# double-add, and prints what it changed.
set -euo pipefail

CONFIG="${WAYBAR_CONFIG:-$HOME/.config/waybar/config.jsonc}"
STYLE="${WAYBAR_STYLE:-$HOME/.config/waybar/style.css}"
HERE="$(cd "$(dirname "$0")" && pwd)"

say()  { printf '\033[32m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

[ -f "$CONFIG" ] || die "waybar config not found at $CONFIG"

if grep -q 'custom/dbforge' "$CONFIG"; then
  warn "custom/dbforge is already in $CONFIG; nothing to do."
  exit 0
fi

# waybar is started by systemd, whose user manager does not have ~/.local/bin
# on PATH -- user services never source ~/.bashrc. A module referring to a bare
# "dbctl" therefore finds nothing and renders an empty widget, silently. Resolve
# the binaries now and write absolute paths into the config.
resolve() {
  local name="$1" p
  if p="$(command -v "$name" 2>/dev/null)"; then
    printf '%s' "$p"
    return 0
  fi
  die "$name is not on PATH; run 'make install' first"
}

DBCTL="$(resolve dbctl)"
TUI="$(resolve dbforge-tui)"
MENU="$(resolve dbforge-waybar-menu)"

# omarchy-launch-tui opens a terminal for the TUI; without it, fall back to a
# plain terminal exec.
if command -v omarchy-launch-tui >/dev/null 2>&1; then
  ON_CLICK="omarchy-launch-tui $TUI"
else
  ON_CLICK="xdg-terminal-exec -e $TUI"
fi

say "Using:"
say "  exec:           $DBCTL status --waybar"
say "  on-click:       $ON_CLICK"
say "  on-click-right: $MENU"

STAMP="$(date +%s)"
cp "$CONFIG" "$CONFIG.pre-dbforge-$STAMP"
say "Backed up $CONFIG -> $CONFIG.pre-dbforge-$STAMP"

# waybar's config is JSONC. Python's json cannot parse comments, so edit the
# text rather than reformatting the file -- reserialising would strip every
# comment the user has.
python3 - "$CONFIG" "$DBCTL" "$ON_CLICK" "$MENU" <<'PY'
import re, sys

path, dbctl, on_click, menu = sys.argv[1:5]
src = open(path).read()

module = '''  "custom/dbforge": {
    "exec": "%s status --waybar",
    "return-type": "json",
    "signal": 8,
    "interval": 300,
    "tooltip": true,
    "on-click": "%s",
    "on-click-right": "%s"
  },
''' % (dbctl, on_click, menu)

# Put the widget at the front of modules-right, next to the other indicators.
m = re.search(r'("modules-right"\s*:\s*\[)', src)
if not m:
    sys.exit("could not find modules-right in the waybar config")
src = src[:m.end()] + '\n    "custom/dbforge",' + src[m.end():]

# Insert the definition after the opening brace of the top-level object.
brace = src.index('{')
src = src[:brace + 1] + '\n' + module + src[brace + 1:]

open(path, 'w').write(src)
print("  added custom/dbforge to modules-right")
PY

if [ -f "$STYLE" ] && ! grep -q 'custom-dbforge' "$STYLE"; then
  cp "$STYLE" "$STYLE.pre-dbforge-$STAMP"
  cat "$HERE/style.css" >> "$STYLE"
  say "Appended styling to $STYLE"
fi

# A bar that is already running has the old config; reload it.
if pkill -SIGUSR2 waybar 2>/dev/null; then
  say "Reloaded waybar"
else
  warn "waybar is not running; it will pick this up next start"
fi

say "Done."
