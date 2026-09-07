VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local

.PHONY: build test test-integration vet lint install uninstall clean

build:
	go build -ldflags "$(LDFLAGS)" -o dist/dbforge ./cmd/dbforge

test:
	go test ./...

# Needs a working rootless Podman; pulls real images.
test-integration:
	go test -tags integration -timeout 20m ./test/integration/

vet:
	go vet ./...

# Install one binary under two names, as spec 5 describes.
install: build
	install -Dm755 dist/dbforge $(PREFIX)/bin/dbforge
	ln -sf dbforge $(PREFIX)/bin/dbforged
	ln -sf dbforge $(PREFIX)/bin/dbctl
	install -Dm644 packaging/dbforged.service \
		$(HOME)/.config/systemd/user/dbforged.service
	@echo
	@echo "Binaries installed. To enable the service, run:"
	@echo "  ./packaging/install.sh"
	@echo "(it enables podman.socket and dbforged, and checks user lingering)"

# Removes the program only. Instance data under ~/.local/share/dbforge is
# deliberately left alone (spec 7, phase 5).
uninstall:
	-systemctl --user disable --now dbforged 2>/dev/null
	rm -f $(PREFIX)/bin/dbforge $(PREFIX)/bin/dbforged $(PREFIX)/bin/dbctl
	rm -f $(HOME)/.config/systemd/user/dbforged.service
	-systemctl --user daemon-reload 2>/dev/null
	@echo
	@echo "Removed the program. Your databases are untouched:"
	@echo "  containers: podman ps -a --filter label=io.dbforge.managed"
	@echo "  data:       ~/.local/share/dbforge"
	@echo "To remove those too, run 'dbctl rm <id> --wipe-data' BEFORE uninstalling."

clean:
	rm -rf dist
