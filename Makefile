.PHONY: all fmt lint vet test test-integration test-all cover cover-check nox clean hooks

all: fmt vet lint test

## Formatting
fmt:
	gofmt -w .
	goimports -w . 2>/dev/null || true

## Linting
lint:
	golangci-lint run --timeout 2m . ./cmd/... ./middleware/... ./internal/...

## Static analysis
vet:
	go vet ./...

## Unit tests only
test:
	go test -short -race -count=1 ./...

## Integration tests (requires Chrome)
test-integration:
	go test -race -timeout 120s -run TestIntegration ./...

## All tests
test-all:
	go test -race -timeout 180s ./...

## Coverage
cover:
	go test -timeout 180s -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Coverctl policy check
cover-check:
	go test -timeout 600s -coverprofile=coverage.out ./...
	coverctl check --config .coverctl.yaml --from-profile --profile coverage.out

## Security scan
nox:
	nox scan . || test -f findings.json
	test "$$(jq '[.findings[] | select(.Status != "baselined")] | length' findings.json)" -eq 0
	cd ui && npm audit --audit-level=moderate
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

## Install pre-commit hook
hooks:
	ln -sf ../../scripts/pre-commit.sh .git/hooks/pre-commit
	@echo "Pre-commit hook installed"

## Clean artifacts
clean:
	rm -f coverage.out coverage.html coverage.txt
	rm -f scout
	rm -f *.png
