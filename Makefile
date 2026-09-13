# Checks for the DBForge Omarchy widget.
#
# Everything here needs a live Omarchy install, since both `omarchy plugin
# validate` and the shell's QML modules ship with it. There is no CI workflow
# for that reason -- a runner without Omarchy has nothing to check against.
#
# QMLLINT names the Qt 6 binary explicitly because the `qmllint` on PATH is, on
# Arch, the Qt 5 build: it reports nothing at all, including on files that do
# not parse. A check that silently passes everything is worse than no check.
OMARCHY_SHELL ?= /usr/share/omarchy/shell
QMLLINT       ?= /usr/lib/qt6/bin/qmllint
QML            = Panel.qml Service.qml DbForgeIcon.qml

.PHONY: check test validate lint install uninstall \
        dbforge dbforge-test dbforge-install

check: test validate lint

# Model.js is plain JavaScript, so the shaping rules are testable without a
# shell to run them in. Skipped rather than failed without node: node is not
# otherwise a dependency of this plugin.
test:
	@if command -v node >/dev/null 2>&1; then \
		node model_test.mjs; \
	else \
		echo "node not found; skipping model_test.mjs"; \
	fi

validate:
	omarchy plugin validate .

# Expect import warnings: qmllint cannot resolve Quickshell's module layout, so
# every panel produces a cascade of them. Omarchy's own first-party plugins
# produce the same warnings in the same categories. Watch for a new *category*,
# not for zero.
lint:
	$(QMLLINT) -I $(OMARCHY_SHELL) $(QML)

install:
	./install.sh

uninstall:
	./uninstall.sh

# The daemon has its own Makefile and its own dependencies (Go, Podman); these
# are passthroughs so you do not have to remember which half you are in. Run
# `make -C dbforge help`-style targets there directly for anything else.
dbforge:
	$(MAKE) -C dbforge build

dbforge-test:
	$(MAKE) -C dbforge test

dbforge-install:
	$(MAKE) -C dbforge install
