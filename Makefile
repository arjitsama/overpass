.PHONY: build test lint run-local smoke accept clean

BIN := bin/agent

build:
	go build -o bin/ ./cmd/agent ./cmd/cardhash ./cmd/passes

test:
	go test -race ./...

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...

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
