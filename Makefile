VERSION ?= dev
REVISION := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/Ember-Moth/aws-panel/internal/app/buildinfo.Version=$(VERSION) -X github.com/Ember-Moth/aws-panel/internal/app/buildinfo.Revision=$(REVISION) -X github.com/Ember-Moth/aws-panel/internal/app/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build run fmt fmt-check vet test test-race assets swagger generated-check check smoke preview compose-smoke

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/aws-panel .

run:
	go run . run

fmt:
	gofmt -w main.go cmd internal pkg web/*.go tests

fmt-check:
	@test -z "$$(gofmt -l main.go cmd internal pkg web/*.go tests)" || (gofmt -l main.go cmd internal pkg web/*.go tests; exit 1)

vet:
	go vet ./...

# The PostgreSQL/Redis integration package runs well past go test's 10-minute default.
TEST_TIMEOUT ?= 30m
RACE_TEST_TIMEOUT ?= 60m

test:
	go test -timeout $(TEST_TIMEOUT) ./...

test-race:
	go test -race -timeout $(RACE_TEST_TIMEOUT) ./...

assets:
	python3 scripts/check-assets.py

swagger:
	sh scripts/generate-swagger.sh

generated-check:
	sh scripts/check-generated.sh

check: fmt-check assets vet test generated-check

smoke: build
	python3 scripts/smoke.py

preview: build
	python3 scripts/smoke.py --preview

compose-smoke:
	python3 scripts/compose-smoke.py --report .local/compose-acceptance.json
