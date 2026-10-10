#!/usr/bin/env bash
# Installs -- or upgrades -- everything DBForge needs, in one go:
#
#   1. the system packages it depends on (pacman), if any are missing
#   2. subuid/subgid ranges for rootless Podman, if the user has none
#   3. DBForge itself: built from source, daemon (re)started on the new binary
#   4. the Omarchy bar widget, put on the bar
#
# Every step checks before it acts, so running it again is how you upgrade.
#
# It works from three places:
#   - a clone of the repo:            ./install-all.sh   (or: make install-all)
#   - piped from the web:             curl -fsSL <raw url>/install-all.sh | bash
#   - the widget's "Install DBForge" row, which runs it from the plugin folder
# Only the first has the source beside it. The other two clone the repo into
# $DBFORGE_SRC (default ~/.local/src/omarchy-dbforge) and run from there --
# never building inside ~/.config/omarchy/plugins, which the shell watches and
# reloads on every write.
#
# Options:
#   --no-enable   install the widget without putting it on the bar
#   --yes, -y     do not ask before installing packages
set -euo pipefail

REPO_URL="${DBFORGE_REPO:-https://github.com/fiifiofosu/omarchy-dbforge}"
SRC_DIR="${DBFORGE_SRC:-$HOME/.local/src/omarchy-dbforge}"
PLUGINS_DIR="${OMARCHY_PLUGIN_DIR:-$HOME/.config/omarchy/plugins}"

ENABLE_ARG="--enable"
ASSUME_YES=0
for arg in "$@"; do
  case "$arg" in
    --no-enable) ENABLE_ARG="--no-enable" ;;
    --yes|-y) ASSUME_YES=1 ;;
    -h|--help)
      sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

say()  { printf '\033[32m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -ne 0 ] || die "run this as your own user, not root: DBForge is rootless"

# Everything below runs inside main(), called on the last line. Piped from
# curl, bash reads this script from stdin as it goes, so stdin is not the
# terminal and sudo and the prompts would have nothing to read from. Wrapping
# the body means bash has the whole script before running any of it, and the
# call can then hand main the terminal as its stdin.
main() {

# --- 1. packages -------------------------------------------------------------
#
# command -> the Arch package that provides it. Go is only needed to build; if
# the packaged Go is older than go.mod asks for, Go fetches the right toolchain
# itself (GOTOOLCHAIN=auto, the default), so any packaged Go is enough.
need=()
for pair in git:git make:make go:go podman:podman wl-copy:wl-clipboard \
            newuidmap:shadow; do
  command -v "${pair%%:*}" >/dev/null 2>&1 || need+=("${pair#*:}")
done

if [ "${#need[@]}" -gt 0 ]; then
  command -v pacman >/dev/null 2>&1 ||
    die "missing: ${need[*]}. Install them with your package manager and re-run."
  say "Installing missing packages: ${need[*]}"
  if [ "$ASSUME_YES" -eq 1 ] || [ ! -t 0 ]; then  # no terminal: nobody to ask
    sudo pacman -S --needed --noconfirm "${need[@]}"
  else
    sudo pacman -S --needed "${need[@]}"
  fi
else
  say "Packages: all present"
fi

# --- 2. subuid / subgid ------------------------------------------------------
#
# Rootless Podman maps container users onto a range of subordinate ids. Arch's
# useradd allocates one by default, but users created some other way can have
# none, and then every container fails to start with an opaque newuidmap
# error. Allocate 65536 ids past the highest range already handed out, so this
# cannot overlap another user's.
next_free_id() {
  awk -F: 'BEGIN { m = 100000 } NF >= 3 { e = $2 + $3; if (e > m) m = e } END { print m }' \
    /etc/subuid /etc/subgid 2>/dev/null || echo 100000
}

if grep -q "^$(id -un):" /etc/subuid 2>/dev/null && grep -q "^$(id -un):" /etc/subgid 2>/dev/null; then
  say "Rootless ids: present"
else
  start=$(next_free_id)
  end=$((start + 65535))
  say "Allocating rootless container ids $start-$end for $(id -un)"
  sudo usermod --add-subuids "$start-$end" --add-subgids "$start-$end" "$(id -un)"
  podman system migrate >/dev/null 2>&1 || true
fi

# --- 3. find the source ------------------------------------------------------
#
# BASH_SOURCE is empty or /dev/fd/* when piped into bash, so there is no "here"
# to build from. Inside the plugin folder there is a "here", but it is the
# wrong place to build.
here=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi

case "$here/" in
  "$PLUGINS_DIR"/*) here="" ;;
esac

if [ -z "$here" ] || [ ! -f "$here/dbforge/Makefile" ]; then
  if [ -d "$SRC_DIR/.git" ]; then
    say "Updating the source in $SRC_DIR"
    git -C "$SRC_DIR" pull --ff-only --quiet ||
      warn "could not update $SRC_DIR; building what is there"
  else
    say "Downloading the source into $SRC_DIR"
    mkdir -p "$(dirname "$SRC_DIR")"
    git clone --quiet "$REPO_URL" "$SRC_DIR"
  fi
  here="$SRC_DIR"
fi

# --- 4. DBForge and the widget -----------------------------------------------
say "Building and installing DBForge"
make -C "$here/dbforge" --no-print-directory install FROM_INSTALL_ALL=1 >/dev/null

if [ "$ASSUME_YES" -eq 1 ]; then
  "$here/dbforge/packaging/install.sh" --yes
else
  "$here/dbforge/packaging/install.sh"
fi

if command -v omarchy >/dev/null 2>&1; then
  "$here/install.sh" "$ENABLE_ARG"
else
  warn "omarchy not found: skipped the bar widget. DBForge itself is installed."
fi

echo
say "All set. $("$HOME/.local/bin/dbctl" version 2>/dev/null || echo DBForge) is running."
}

if [ ! -t 0 ] && { : </dev/tty; } 2>/dev/null; then
  main </dev/tty
else
  main
fi
