VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X laramonster/internal/core.Version=$(VERSION)
DIST    := dist

# Linux desktop builds use GTK3 + webkit2gtk-4.1 (drop the tag for GTK4/webkitgtk-6.0).
LINUX_APP_TAGS ?= gtk3

.PHONY: all cli app test vet install uninstall release clean

all: cli app

cli:
	go build -ldflags "$(LDFLAGS)" -o bin/laramonster ./cmd/laramonster

# "production" turns off the web inspector and its right-click menu in the app.
app:
	go build -tags "$(LINUX_APP_TAGS) production" -ldflags "$(LDFLAGS)" -o bin/laramonster-app ./cmd/laramonster-app

test:
	go test -tags $(LINUX_APP_TAGS) ./...

vet:
	go vet -tags $(LINUX_APP_TAGS) ./...

# Install for the current user on Linux (~/.local/bin + app menu entry).
install: all
	./scripts/install-linux.sh $(if $(DESKTOP),--desktop-shortcut)

uninstall:
	./scripts/install-linux.sh --uninstall

# Cross-compiled CLI for every platform, plus the Windows desktop app (no cgo).
# The macOS and Linux desktop apps need cgo, so build them on those systems.
release:
	rm -rf $(DIST) && mkdir -p $(DIST)
	for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $(DIST)/laramonster-$$os-$$arch$$ext ./cmd/laramonster || exit 1; \
	done
	GOOS=windows GOARCH=amd64 go build -tags production -ldflags "$(LDFLAGS) -H windowsgui" -o $(DIST)/LaraMonster-windows-amd64.exe ./cmd/laramonster-app
	cd $(DIST) && sha256sum * > SHA256SUMS

clean:
	rm -rf bin $(DIST)
