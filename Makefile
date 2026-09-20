.PHONY: fmt vet test build race

fmt:
	gofmt -l .

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./pkg/pipeline ./pkg/tui ./pkg/index ./pkg/replicate ./pkg/retention ./pkg/manifest

build:
	go build ./cmd/backup-engine
