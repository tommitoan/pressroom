.PHONY: build test e2e contract docker-build docker-run vet fmt fmt-check run tidy check

build:
	go build -o bin/pressroom ./cmd/pressroom

test:
	go test -race -count=1 ./...

# Drives a real browser; set CHROME_PATH (and CHROMIUM_NO_SANDBOX=true where needed).
e2e:
	PRESSROOM_E2E=1 go test -race -count=1 ./internal/render ./internal/api

# Runs the contract test against a running service: PRESSROOM_URL and PRESSROOM_TOKEN must be set.
contract:
	go test -count=1 -v -run Contract ./examples/client

docker-build:
	docker build -t pressroom:local .

# Needs PRESSROOM_TOKEN in the environment (at least 16 characters).
docker-run:
	docker run --rm -p 8080:8080 -e PRESSROOM_TOKEN pressroom:local

vet:
	go vet ./...

fmt:
	gofmt -w .

# Fails when any file needs formatting; prints the names.
fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

run:
	go run ./cmd/pressroom

tidy:
	go mod tidy

# Everything CI runs.
check: fmt-check vet test build
