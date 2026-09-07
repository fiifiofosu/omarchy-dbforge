# AUR packaging

`dbforge-git/` is the package. It builds from the tip of `main`, which is the
right shape while there are no tagged releases: nothing to publish, nothing to
checksum, and `pkgver()` derives a monotonic version from `git describe`.

## Checking it

```
make hook-test               # the pacman install hooks, no system changes
make pkgbuild-check          # the above, then builds and inspects the package
packaging/aur/check.sh --install   # and installs it
```

`check.sh` swaps the source for a `file://` clone of this repository so it
checks the code in front of you, and asserts on the paths the package must
contain. Everything else in the PKGBUILD runs unmodified, so a failure there is
a real failure. It packages `HEAD`, not the working tree — commit first.

## Publishing

```
git clone ssh://aur@aur.archlinux.org/dbforge-git.git aur-dbforge-git
cp packaging/aur/dbforge-git/{PKGBUILD,dbforge.install} aur-dbforge-git/
cd aur-dbforge-git && makepkg --printsrcinfo > .SRCINFO
git add -A && git commit && git push
```

`.SRCINFO` is what the AUR indexes and is regenerated from the PKGBUILD, so it
is not kept in this repository — it would only ever be a stale copy. `make
srcinfo` writes it next to the PKGBUILD when you need it.

## Why not `dbforge-bin`

A `-bin` package needs published release tarballs with checksums to point at.
There are none yet. When there are, `make dist-tarball` produces one from a
tag, and the `-bin` PKGBUILD is a short file next to this one — the packaging
layout (`make DESTDIR=... PREFIX=/usr install`) is already what it would use.
