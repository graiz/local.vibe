BINARY  := vibe
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install test clean setup dev

build:
	go build $(LDFLAGS) -o $(BINARY) .

install: build
	./$(BINARY) install

test:
	go test ./...

clean:
	rm -f $(BINARY)

setup: install
	sudo ./$(BINARY) setup

dev: build
	./$(BINARY) dev
