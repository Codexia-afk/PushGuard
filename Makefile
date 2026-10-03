.PHONY: build install test vet fmt ready check pre-push demo release

build:
	go build -trimpath -o bin/pushguard ./cmd/pushguard

install:
	go install ./cmd/pushguard

test:
	go test -count=1 ./...

# check never formats or edits source. It is the same entrypoint CI uses.
check: ready

ready:
	go run ./cmd/pushguard ready --non-interactive

# Safe gate for repositories that keep the final push under normal Git control.
pre-push:
	go run ./cmd/pushguard ready --non-interactive

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

demo:
	go run ./cmd/pushguard demo

release:
	go run ./cmd/release
