.PHONY: build test vet fmt lint complexity security licenses tidy ci clean all \
	release-check validate-workflows \
	docs-validate docs-nav docs-lychee docs-build docs-serve docs-install

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)
BIN ?= spectra

# --- Build -------------------------------------------------------------------

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/spectra/

# --- Quality -----------------------------------------------------------------

test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -s -w .

lint:
	golangci-lint run

complexity:
	gocyclo -over 15 -ignore '_test\.go$$' .

security:
	gosec -exclude=G104,G204,G304,G602,G703,G704 ./...

licenses:
	go-licenses report ./...

tidy:
	go mod tidy

ci: vet test complexity lint security licenses docs-validate

release-check:
	bash scripts/release-check.sh

validate-workflows:
	bash scripts/validate-workflows.sh

# --- Documentation -----------------------------------------------------------

docs-nav:
	./scripts/validate_mkdocs_nav.sh

docs-lychee:
	./scripts/lychee/validate_lychee.sh

docs-build:
	mkdocs build --strict

docs-serve:
	mkdocs serve --dev-addr 127.0.0.1:8080

docs-install:
	pip install --user mkdocs mkdocs-material
	./scripts/lychee/install_lychee.sh

docs-validate: docs-nav docs-lychee docs-build

# --- Cleanup -----------------------------------------------------------------

clean:
	rm -f $(BIN) junit-report.xml gosec-report.xml
	rm -rf site/

all: build vet test docs-validate
