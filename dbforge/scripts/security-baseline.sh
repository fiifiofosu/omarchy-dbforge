#!/usr/bin/env bash
#
# Produces the security-baseline evidence for an exact commit.
#
# This exists because "we fixed it" is not evidence. The two supply-chain
# findings against DBForge were both about a mutable pointer deciding which
# code runs: a registry tag for the database images, and releases/latest for
# the self-updater. Neither is the kind of thing a reader can confirm from a
# diff alone, so this re-establishes both properties mechanically, against the
# tree as committed, and prints the commit it did it at.
#
# Run it on a clean checkout of the commit being submitted:
#
#   make security-baseline
#
# It writes dist/security-baseline.txt and exits nonzero if any check fails.
# Checks that reach the registry need network; they are marked, and a run
# without network fails rather than quietly reporting less.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
mkdir -p dist
out=dist/security-baseline.txt

fails=0
say() { printf '%s\n' "$*" | tee -a "$out"; }
pass() { say "PASS  $*"; }
fail() { say "FAIL  $*"; fails=$((fails + 1)); }

# code strips comment lines from a grep -rn result. Several of these checks
# search for the name of something that was removed, and the code that removed
# it documents at length why -- a doc comment naming SHA256SUMS is the opposite
# of the thing being looked for.
code() { grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' || true; }

# absent fails the named check if the search found anything, and prints what it
# found, so a failure says which line to go and look at.
absent() {
  local what=$1 hits=$2
  if [ -n "$hits" ]; then
    fail "$what"
    printf '%s\n' "$hits" | sed 's/^/      /' | tee -a "$out" >/dev/null
    printf '%s\n' "$hits" | sed 's/^/      /'
  else
    pass "$what"
  fi
}

: >"$out"
say "DBForge security baseline"
say "generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"

commit=$(git rev-parse HEAD)
say "commit:    $commit"
if [ -n "$(git status --porcelain)" ]; then
  say "tree:      DIRTY -- this evidence does not describe commit $commit"
  say ""
  fail "working tree is dirty; run this on a clean checkout of the commit being submitted"
else
  say "tree:      clean"
fi
say ""

# ---------------------------------------------------------------------------
say "1. Engine images are pinned to immutable digests"
say ""

# Every pin is a digest, and every catalogued engine has at least one. The Go
# tests assert this too; repeating it here keeps the evidence self-contained
# for a reader who is not running the suite.
pinned=$(grep -oE '"sha256:[0-9a-f]{64}"' internal/engines/pins.go | tr -d '"' | sort -u)
count=$(printf '%s\n' "$pinned" | grep -c . || true)
if [ "$count" -gt 0 ]; then
  pass "pins.go pins $count distinct digests"
else
  fail "pins.go pins nothing"
fi

# The hole that was reported: constructing image + ":" + tag and pulling it.
# Nothing outside the pin table and the version parser may join an image to a
# tag with a colon.
absent "no code builds an image reference from a tag" \
  "$(grep -rnE '\.Image \+ ":"' --include='*.go' . | code)"

# What the runtime is handed must come from PinnedRef and nowhere else.
callers=$(grep -rln 'PullImage(' --include='*.go' internal/ | grep -v '/runtime/' | sort)
if [ "$callers" = "internal/daemon/manager.go" ]; then
  pass "only internal/daemon/manager.go pulls images"
else
  fail "unexpected pull sites: $callers"
fi

# Network: every pinned digest must still be fetchable by digest. A pin the
# registry has garbage-collected is a pin that cannot be installed from.
say ""
say "  resolving each pin against the registry (needs network):"
while read -r engine repo; do
  [ -n "$engine" ] || continue
  tok=$(curl -fsS "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$repo:pull" \
        | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  if [ -z "$tok" ]; then
    fail "  could not authenticate to the registry for $repo"
    continue
  fi
  # The digests belonging to this engine, read back out of the generated file.
  for d in $(awk -v e="\"$engine\":" '
        $0 ~ "^\t"e { inblock=1; next }
        inblock && /^\t}/ { inblock=0 }
        inblock { print }' internal/engines/pins.go \
      | grep -oE 'sha256:[0-9a-f]{64}'); do
    code=$(curl -s -o /dev/null -w '%{http_code}' -I -H "Authorization: Bearer $tok" \
             -H 'Accept: application/vnd.oci.image.index.v1+json' \
             -H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json' \
             "https://registry-1.docker.io/v2/$repo/manifests/$d")
    if [ "$code" = "200" ]; then
      pass "  $repo@${d:0:19}... resolves"
    else
      fail "  $repo@${d:0:19}... returned HTTP $code"
    fi
  done
done <<'ENGINES'
postgres library/postgres
mysql    library/mysql
mariadb  library/mariadb
redis    library/redis
ENGINES

# ---------------------------------------------------------------------------
say ""
say "2. The published runtime contains no self-updater"
say ""

# The updater may read a version number. It may not fetch anything, write
# anything, or run anything.
absent "internal/update neither writes to disk nor executes anything" \
  "$(grep -rnE '\b(os\.Rename|os\.Create|os\.OpenFile|os\.WriteFile|os\.MkdirTemp|exec\.Command|exec\.CommandContext)\b' \
       --include='*.go' internal/update/ | grep -v '_test.go' | code)"

absent "internal/update does not read release asset URLs" \
  "$(grep -rn 'browser_download_url\|SHA256SUMS' --include='*.go' internal/update/ | grep -v '_test.go' | code)"

# The TUI used to install from a keystroke. Nothing outside internal/update may
# reach the release API at all.
absent "only internal/update contacts the release API" \
  "$(grep -rn 'api\.github\.com' --include='*.go' . | grep -v 'internal/update/' | code)"

# ---------------------------------------------------------------------------
say ""
say "3. The tree builds and its tests pass"
say ""

for target in vet test build; do
  if make "$target" >>"$out" 2>&1; then
    pass "make $target"
  else
    fail "make $target"
  fi
done

say ""
if [ "$fails" -eq 0 ]; then
  say "RESULT: baseline clean at $commit"
else
  say "RESULT: $fails check(s) failed at $commit"
fi
say ""
say "Evidence written to $out"
exit $((fails > 0))
