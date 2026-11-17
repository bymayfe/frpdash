BINARY  := frpdash
PKG     := github.com/bymayfe/frpdash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

export CGO_ENABLED := 0

# Tailwind CSS standalone CLI; the generated web/static/app.css is committed,
# so building the binary does not need it.
TAILWIND_VERSION := v4.3.5
TAILWIND         := bin/tools/tailwindcss-$(TAILWIND_VERSION)

.PHONY: build build-linux deploy-server release css clean

# bin/frpdash for this machine.
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/frpdash

# bin/frpdash-linux-amd64 and bin/frpdash-linux-arm64.
build-linux:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-linux-amd64 ./cmd/frpdash
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY)-linux-arm64 ./cmd/frpdash

# Install or update the server on a VDS over SSH, built from this checkout.
#   make deploy-server [VDS=<ssh host>] [PUBLIC_HOST=<ip or name>]
deploy-server: build-linux
	scripts/deploy-server.sh "$(VDS)" "$(PUBLIC_HOST)"

# Publish a GitHub release with the linux binaries and checksums (used by install.sh).
#   make release VERSION=v0.1.0
release:
	scripts/release.sh "$(VERSION)"

# Rebuild the panel stylesheet after changing templates or web/tailwind.css.
css: $(TAILWIND)
	$(TAILWIND) -i web/tailwind.css -o web/static/app.css --minify

$(TAILWIND):
	scripts/fetch-tailwind.sh $(TAILWIND_VERSION) $@

clean:
	rm -rf bin
