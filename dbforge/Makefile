VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Podman's Go bindings drag in two cgo packages we never execute: gpgme, for
# verifying image signatures, and the btrfs graph driver. Both are the daemon's
# job -- we are an HTTP client of podman, not a second copy of it -- but they
# are in the import graph all the same, and each needs C headers (gpgme.h,
# btrfs/version.h) that a stock CI runner does not have. These tags swap gpgme
# for Go's own OpenPGP and drop the btrfs driver, which leaves the build with
# no C dependencies at all.
#
# They must be used everywhere or nowhere: a build with different tags is a
# different import graph. Hence one definition, used by every target here, by
# CI, and by the PKGBUILD.
GOTAGS  ?= containers_image_openpgp,exclude_graphdriver_btrfs
TESTTAGS := integration,$(GOTAGS)

# PREFIX defaults to a per-user install. Packaging overrides both, e.g.
#   make DESTDIR="$pkgdir" PREFIX=/usr install
PREFIX  ?= $(HOME)/.local
DESTDIR ?=
BINDIR  := $(PREFIX)/bin

# A packaged install owns /usr/lib/systemd/user; a per-user one owns
# ~/.config/systemd/user. Which applies is decided by PREFIX.
ifeq ($(PREFIX),/usr)
UNITDIR := $(PREFIX)/lib/systemd/user
else
UNITDIR := $(HOME)/.config/systemd/user
endif

.PHONY: build test test-integration vet install install-unit uninstall \
        dist-tarball srcinfo pkgbuild-check hook-test clean

build:
	go build -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o dist/dbforge ./cmd/dbforge
	go build -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o dist/dbforge-tui ./cmd/dbforge-tui

test:
	go test -tags "$(GOTAGS)" -race ./...

# Needs a working rootless Podman; pulls real images.
test-integration:
	go test -tags "$(TESTTAGS)" -timeout 20m ./test/integration/

vet:
	go vet -tags "$(GOTAGS)" ./...
	go vet -tags "$(TESTTAGS)" ./...

# The unit's ExecStart and PATH depend on where the binaries land, so it is
# rendered rather than copied. @BINDIR@ is the only substitution.
# A packaged install's BINDIR is already /usr/bin, so listing it again would
# just duplicate the entry.
ifeq ($(PREFIX),/usr)
UNITPATH := /usr/local/bin:/usr/bin
else
UNITPATH := $(BINDIR):/usr/local/bin:/usr/bin
endif

dist/dbforged.service: packaging/dbforged.service.in
	@mkdir -p dist
	sed -e 's|@BINDIR@|$(BINDIR)|g' -e 's|@PATH@|$(UNITPATH)|g' $< > $@

install: build dist/dbforged.service
	install -Dm755 dist/dbforge $(DESTDIR)$(BINDIR)/dbforge
	install -Dm755 dist/dbforge-tui $(DESTDIR)$(BINDIR)/dbforge-tui
	install -Dm755 packaging/waybar/dbforge-waybar-menu \
		$(DESTDIR)$(BINDIR)/dbforge-waybar-menu
	ln -sf dbforge $(DESTDIR)$(BINDIR)/dbforged
	ln -sf dbforge $(DESTDIR)$(BINDIR)/dbctl
	install -Dm644 dist/dbforged.service $(DESTDIR)$(UNITDIR)/dbforged.service
	install -Dm644 packaging/waybar/module.jsonc \
		$(DESTDIR)$(PREFIX)/share/dbforge/waybar/module.jsonc
	install -Dm644 packaging/waybar/style.css \
		$(DESTDIR)$(PREFIX)/share/dbforge/waybar/style.css
	install -Dm755 packaging/waybar/install.sh \
		$(DESTDIR)$(PREFIX)/share/dbforge/waybar/install.sh
	install -Dm644 README.md $(DESTDIR)$(PREFIX)/share/doc/dbforge/README.md
	install -Dm644 LICENSE $(DESTDIR)$(PREFIX)/share/licenses/dbforge/LICENSE
	@[ -n "$(DESTDIR)" ] || $(MAKE) --no-print-directory post-install-notes

post-install-notes:
	@echo
	@echo "Binaries installed to $(BINDIR). To enable the service, run:"
	@echo "  ./packaging/install.sh"
	@echo "(it enables podman.socket and dbforged, and checks user lingering)"
	@echo
	@echo "For the waybar widget:"
	@echo "  ./packaging/waybar/install.sh"

# Removes the program only. Instance data under ~/.local/share/dbforge is
# deliberately left alone (spec 7, phase 5).
uninstall:
	-systemctl --user disable --now dbforged 2>/dev/null
	rm -f $(BINDIR)/dbforge $(BINDIR)/dbforge-tui \
		$(BINDIR)/dbforged $(BINDIR)/dbctl \
		$(BINDIR)/dbforge-waybar-menu
	rm -f $(UNITDIR)/dbforged.service
	rm -rf $(PREFIX)/share/dbforge
	-systemctl --user daemon-reload 2>/dev/null
	@echo
	@echo "Removed the program. Your databases are untouched:"
	@echo "  containers: podman ps -a --filter label=io.dbforge.managed"
	@echo "  data:       ~/.local/share/dbforge"
	@echo "To remove those too, run 'dbctl rm <id> --wipe-data' BEFORE uninstalling."

# Source tarball for a versioned AUR package. Uses git archive so it contains
# exactly what is committed -- no dist/, no stray local files.
dist-tarball:
	@mkdir -p dist
	git archive --format=tar.gz --prefix=dbforge-$(VERSION)/ \
		-o dist/dbforge-$(VERSION).tar.gz HEAD
	@echo "dist/dbforge-$(VERSION).tar.gz"

# .SRCINFO is what the AUR indexes; it must be regenerated whenever the
# PKGBUILD changes or the upload is rejected.
srcinfo:
	cd packaging/aur/dbforge-git && makepkg --printsrcinfo > .SRCINFO
	@echo "regenerated packaging/aur/dbforge-git/.SRCINFO"

# Builds the package for real and lists what it would install. Catches a
# PKGBUILD that references a file that moved, which is the usual way these rot.
pkgbuild-check:
	./packaging/aur/check.sh

# The install hooks only ever run on a real install, so they get their own
# test: stub systemctl, source them, assert on the calls.
hook-test:
	./packaging/aur/hook-test.sh

clean:
	rm -rf dist
