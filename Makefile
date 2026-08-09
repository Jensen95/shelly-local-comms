.DEFAULT_GOAL := build

.PHONY: build test cover fmt vet lint tui serve

build:
	go build ./...

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

fmt:
	gofmt -w $$(git ls-files '*.go')

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed."; \
		echo "Install: https://golangci-lint.run/welcome/install/"; \
	fi

tui:
	go run ./cmd/shellyctl tui

serve:
	go run ./cmd/shellyctl serve
