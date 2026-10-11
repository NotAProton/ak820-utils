BINARY := bin/ak820-utils
PREFIX ?= ~/.local

.PHONY: build test vet fmt install clean

build: vet test
	$(GO) build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/ak820-utils

GO ?= go
test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

install: build
	install -m 0755 $(BINARY) $(PREFIX)/bin/ak820-utils

clean:
	rm -rf bin
