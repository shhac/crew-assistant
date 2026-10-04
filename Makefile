BINARY := crew-assistant
# Invoke npm through Node: env-based npm launchers can fail in a command sandbox.
# Both commands remain configurable; npm's CLI is resolved from PATH.
NODE ?= node
NPM ?= $(NODE) "$$(command -v npm)"
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build dashboard test test-race check check-ui-types check-ui-bundle check-ui-tests dev
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/crew-assistant

# A generous ceiling protects the team's background-priority checks running
# beside other work, even when packages are fast in isolation.
TEST_TIMEOUT ?= 30m

dashboard:
	$(NPM) --prefix internal/dashboard/ui run build

test:
	go test ./... -count=1 -timeout $(TEST_TIMEOUT)

test-race:
	go test -race ./... -count=1 -timeout $(TEST_TIMEOUT)

check:
	TEST_TIMEOUT=$(TEST_TIMEOUT) go run ./cmd/check-project

check-ui-types:
	$(NPM) --prefix internal/dashboard/ui run check

check-ui-bundle:
	$(NPM) --prefix internal/dashboard/ui run check:bundle

check-ui-tests:
	$(NPM) --prefix internal/dashboard/ui test

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
