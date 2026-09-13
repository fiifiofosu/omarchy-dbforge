#!/usr/bin/env bash
# Builds the AUR package from the local checkout and reports what it installs.
#
# The PKGBUILD points at the GitHub remote, which is private and in any case
# only has what has been pushed. To check the PKGBUILD against the code in
# front of you, this swaps the source for a file:// clone of this repo. Nothing
# else about the PKGBUILD is altered, so a failure here is a real failure.
#
# Usage: packaging/aur/check.sh [--install]
set -euo pipefail

REPO="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

say() { printf '\033[32m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[31m==>\033[0m %s\n' "$*" >&2; exit 1; }

command -v makepkg >/dev/null || fail "makepkg not found (install pacman's base-devel)"

# A developer's Arch box has podman's build dependencies, so a PKGBUILD that
# builds or tests without the cgo-avoiding tags succeeds here and fails in a
# clean chroot. Routing every go invocation through make is what keeps the tags
# in one place, so check that nothing has slipped back to calling go directly.
say "Checking the PKGBUILD builds through make"
if grep -nE '^[[:space:]]*go (build|test)' "$REPO/packaging/aur/dbforge-git/PKGBUILD"; then
  fail "PKGBUILD calls go directly; use a make target so GOTAGS applies"
fi
printf '    ok      no direct go invocations\n'

say "Checking the pacman install hooks"
"$REPO/packaging/aur/hook-test.sh" | sed 's/^/    /'

cp "$REPO/packaging/aur/dbforge-git/PKGBUILD" \
   "$REPO/packaging/aur/dbforge-git/dbforge.install" "$WORK/"

# file:// rather than the remote, so this checks the working tree's HEAD.
sed -i "s|git+\$url.git|git+file://$REPO|" "$WORK/PKGBUILD"

say "Building in $WORK (this compiles and runs the unit tests)"
( cd "$WORK" && makepkg --nodeps --noconfirm --force )

# makepkg also emits dbforge-git-debug, which contains only debug symbols.
PKGFILE="$(find "$WORK" -maxdepth 1 -name '*.pkg.tar.*' ! -name '*-debug-*' -print -quit)"
[ -n "$PKGFILE" ] || fail "makepkg produced no package"
say "Checking $(basename "$PKGFILE")"

say "Package contents:"
bsdtar -tf "$PKGFILE" | grep -v '^\.' | sort | sed 's/^/    /'

# The things a broken PKGBUILD silently drops. Each of these is load-bearing:
# without the unit there is no service, without the symlinks neither command
# name resolves, and without the waybar files the widget installer has nothing
# to install.
say "Checking for required paths"
required=(
  usr/bin/dbforge
  usr/bin/dbforged
  usr/bin/dbctl
  usr/bin/dbforge-tui
  usr/bin/dbforge-waybar-menu
  usr/lib/systemd/user/dbforged.service
  usr/share/dbforge/waybar/install.sh
  usr/share/dbforge/waybar/module.jsonc
  usr/share/licenses/dbforge/LICENSE
)
missing=0
for path in "${required[@]}"; do
  if bsdtar -tf "$PKGFILE" | grep -qx "$path"; then
    printf '    ok      %s\n' "$path"
  else
    printf '    MISSING %s\n' "$path"
    missing=1
  fi
done
[ "$missing" -eq 0 ] || fail "package is missing required paths"

# The unit must point at the packaged binary, not at whatever ~/.local/bin the
# person running makepkg happens to have.
say "Checking the rendered unit"
bsdtar -xOf "$PKGFILE" usr/lib/systemd/user/dbforged.service > "$WORK/unit"
grep -qx 'ExecStart=/usr/bin/dbforged' "$WORK/unit" \
  || fail "unit ExecStart is wrong: $(grep ExecStart "$WORK/unit")"
if grep -qE '@BINDIR@|@PATH@' "$WORK/unit"; then
  fail "unit has an unsubstituted placeholder: $(grep -E '@BINDIR@|@PATH@' "$WORK/unit")"
fi
printf '    ok      ExecStart=/usr/bin/dbforged\n'

if [ "${1:-}" = "--install" ]; then
  say "Installing $PKGFILE"
  sudo pacman -U --noconfirm "$PKGFILE"
else
  # dist/ may not exist yet -- makepkg builds in its own temp directory, so
  # nothing here has necessarily created it. Errors are not swallowed: CI
  # uploads this file as an artefact, and a silently skipped copy turned into
  # a green build that published nothing.
  mkdir -p "$REPO/dist"
  cp "$PKGFILE" "$REPO/dist/"
  say "OK. Package left in dist/$(basename "$PKGFILE")"
  say "Re-run with --install to install it."
fi
