.PHONY: help check test test-race test-nocgo test-client test-native test-demo build client app package-app

help:
	@printf '%s\n' \
	  'make check         Format, script syntax, JavaScript and repository checks' \
	  'make test          Go tests and static analysis (no real calls)' \
	  'make test-race     Go race detector (native cgo dependencies required)' \
	  'make test-nocgo    Portable tests without native audio/USB' \
	  'make test-client   Isolated Swift service lifecycle tests' \
	  'make test-native   Native call panel harness (no real calls)' \
	  'make test-demo     Isolated HTTP smoke check of the built service' \
	  'make build         Build the macOS service for this Mac' \
	  'make client        Build the Swift menu bar client' \
	  'make app           Bundle the complete macOS app' \
	  'make package-app VERSION=v1.1.0   Archive this Mac architecture'

check:
	sh scripts/check-source.sh

test:
	go test ./... -count=1 -timeout=90s
	go vet ./...

test-race:
	go test -race ./... -count=1 -timeout=90s

test-nocgo:
	CGO_ENABLED=0 go test ./... -count=1 -timeout=90s

test-client:
	sh scripts/test-macos-client.sh

test-native:
	DJ4HUB_TEST_NATIVE_PANEL=1 go test ./cmd/dj4ghub-macos -run TestNativeCallPanelTransitions -count=1 -timeout=60s

test-demo:
	node scripts/check-demo.cjs

build:
	sh scripts/build-macos.sh

client:
	sh scripts/build-macos-client.sh

app:
	sh scripts/build-macos-app.sh

package-app:
	sh scripts/package-macos-app.sh "$(or $(VERSION),dev)"
