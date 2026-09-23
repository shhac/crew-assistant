BINARY := crew-assistant
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build dashboard test test-race check dev
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/crew-assistant

dashboard:
	npm --prefix internal/dashboard/ui run build

test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

check:
	go vet ./...
	go test ./... -count=1
	npm --prefix internal/dashboard/ui run check
	npm --prefix internal/dashboard/ui test

dev:
	go run ./cmd/crew-assistant $(ARGS)

.PHONY: release release-check
release: release-check
	@echo "Next: git tag $(VERSION) && git push origin main $(VERSION)"

release-check:
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$$' || (echo "Set VERSION=vX.Y.Z"; exit 1)
	@git check-ref-format "refs/tags/$(VERSION)"
	@! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null || (echo "tag $(VERSION) already exists"; exit 1)
	@remote_tag=$$(git ls-remote --tags origin "refs/tags/$(VERSION)") || exit $$?; test -z "$$remote_tag" || (echo "remote tag $(VERSION) already exists"; exit 1)
	@test -z "$$(git status --short)" || (echo "working tree is dirty"; exit 1)
	$(MAKE) dashboard
	@git diff --exit-code -- internal/dashboard/assets
	$(MAKE) check
	$(MAKE) test-race
	@echo "Checks passed. Create and push the version tag when a release is intended."
