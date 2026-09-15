VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: build test vet lint check clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o watchfor-agent ./cmd/watchfor-agent

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	go run honnef.co/go/tools/cmd/staticcheck@2025.1.1 ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: build
	./watchfor-agent check

clean:
	rm -f watchfor-agent
