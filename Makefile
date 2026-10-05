# make build     the CLI for this machine, in ./pantech
# make build-staging  the same CLI against staging (below)
# make test      vet and test
# make dist      every platform's release archive, SHA256SUMS and latest.txt in ./dist,
#                laid out as install.sh expects: dist/<version>/pantech_<os>_<arch>.tar.gz
#                (and dist/<version>/install, that version's own installer)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/Pantech-Dynamics/pantech-cli/internal/cli.Version=$(VERSION)
PLATFORMS := darwin_arm64 darwin_amd64 linux_amd64 linux_arm64

.PHONY: build build-staging test dist clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o pantech ./cmd/pantech

# make build-staging   ./pantech-staging: the same CLI against staging, with its
#                      own profiles (~/.config/pantech-staging) and keychain entries.
#                      STAGING_CONSOLE_URL=https://localhost:3000 for a local console.
STAGING_API_URL ?= https://api-dev.pantechdynamics.com
STAGING_CONSOLE_URL ?= https://staging.console.pantechdynamics.com
build-staging:
	go build -trimpath -o pantech-staging -ldflags "-s -w \
		-X github.com/Pantech-Dynamics/pantech-cli/internal/cli.Version=staging-$(VERSION) \
		-X github.com/Pantech-Dynamics/pantech-cli/internal/api.DefaultBaseURL=$(STAGING_API_URL) \
		-X github.com/Pantech-Dynamics/pantech-cli/internal/cli.DefaultConsoleURL=$(STAGING_CONSOLE_URL) \
		-X github.com/Pantech-Dynamics/pantech-cli/internal/config.Name=pantech-staging" ./cmd/pantech

test:
	go vet ./...
	go test ./...

dist: clean
	@mkdir -p dist/$(VERSION)
	@for p in $(PLATFORMS); do \
		os=$${p%_*}; arch=$${p#*_}; dir=$$(mktemp -d); \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/pantech ./cmd/pantech || exit 1; \
		tar -czf dist/$(VERSION)/pantech_$$p.tar.gz -C $$dir pantech; rm -rf $$dir; \
		echo "built pantech_$$p.tar.gz"; \
	done
	@cd dist/$(VERSION) && shasum -a 256 *.tar.gz > SHA256SUMS
	@echo $(VERSION) > dist/latest.txt
	@cp install.sh dist/install
	@# Each version carries its own installer, so a pre-release can be tried end to end.
	@cp install.sh dist/$(VERSION)/install
	@echo "dist/: upload its contents to the download URL install.sh reads"

clean:
	rm -rf dist pantech pantech-staging
