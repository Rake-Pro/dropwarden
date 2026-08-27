# Thin Makefile shim over the Taskfile, for muscle memory. Prefer `task <name>`.

.PHONY: test vet check build build-win dev doctor discover watch report

test:
	go test ./...

vet:
	go vet ./...

check:
	go vet ./... && go test ./... && GOOS=windows go build ./...

build:
	go build -o bin/dropwarden-cli ./cmd/dropwarden-cli

build-win:
	GOOS=windows go build ./...

doctor:
	go run ./cmd/dropwarden-cli doctor

discover:
	go run ./cmd/dropwarden-cli discover

watch:
	go run ./cmd/dropwarden-cli watch

report:
	go run ./cmd/dropwarden-cli report
