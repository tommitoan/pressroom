.PHONY: build test vet fmt fmt-check run tidy check

build:
	go build -o bin/pressroom ./cmd/pressroom

test:
	go test -race -count=1 ./...

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
