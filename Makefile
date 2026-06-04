APP_NAME   = pgloader
BUILDDIR   = build
GO         = go

.PHONY: all build test clean lint fmt

all: build

build:
	$(GO) build -o $(BUILDDIR)/bin/$(APP_NAME) ./cmd/$(APP_NAME)

test:
	$(GO) test ./internal/... -v -count=1

test-short:
	$(GO) test ./internal/... -short -count=1

lint:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(BUILDDIR)
	rm -f $(APP_NAME)
