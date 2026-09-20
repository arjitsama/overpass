.PHONY: build test lint run-local smoke accept clean trust-up trust-down preflight

BIN := bin/agent
TRUST_DIR := third_party/agent-trust-discovery

build:
	go build -o bin/ ./cmd/agent ./cmd/cardhash ./cmd/passes ./cmd/satreg ./cmd/battery ./cmd/trustseed

# Start the forked trust index locally on :8080 (admin auth off for the demo).
trust-up:
	@mkdir -p $(TRUST_DIR)/data
	cd $(TRUST_DIR) && go run ./cmd/agent-trust-discovery -config config/overpass-local.runtime.yaml

trust-down:
	@pkill -f 'agent-trust-discovery -config' || true

test:
	go test -race ./...

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	scripts/secret-scan.sh

# Full pre-ship gate: tests, the honest-station battery, and a local smoke.
preflight:
	scripts/preflight.sh

run-local: build
	scripts/run-local.sh

# Checks agents started with `make run-local` in another terminal.
smoke:
	scripts/smoke.sh

# Every phase's acceptance script, in order.
accept:
	@for f in scripts/accept/phase-*.sh; do echo "### $$f"; $$f || exit 1; done

clean:
	rm -rf bin .run
