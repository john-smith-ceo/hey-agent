.PHONY: build test install run release-check

build:
	go build -o bin/hey-agent ./cmd/hey-agent

test:
	go test ./...

install: build
	./bin/hey-agent install

run:
	hey-agent listen

release-check: test build
	git diff --check
