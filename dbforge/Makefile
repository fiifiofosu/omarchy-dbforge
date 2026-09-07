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
	@echo "Installed. Next:"
	@echo "  systemctl --user enable --now podman.socket"
	@echo "  systemctl --user enable --now dbforged"

# Removes the program only. Instance data under ~/.local/share/dbforge is
# deliberately left alone (spec 7, phase 5).
uninstall:
	rm -f $(PREFIX)/bin/dbforge $(PREFIX)/bin/dbforged $(PREFIX)/bin/dbctl
	rm -f $(HOME)/.config/systemd/user/dbforged.service
	@echo "Instance data left intact at ~/.local/share/dbforge"

clean:
	rm -rf dist
