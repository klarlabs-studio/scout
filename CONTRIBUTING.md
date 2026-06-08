# Contributing to Scout

Thanks for your interest in contributing to Scout (scout).

## Development Setup

```bash
git clone https://github.com/klarlabs-studio/scout.git
cd scout
make hooks    # install pre-commit hook
make all      # fmt, vet, lint, test
```

Requires Go 1.23+ and Chrome/Chromium installed.

## Making Changes

1. Fork the repo and create a branch from `main`
2. Write code — `gofmt` runs automatically via pre-commit hook
3. Add tests for new functionality
4. Run `make test-all` (requires Chrome for integration tests)
5. Run `make lint` to check for issues
6. Open a pull request

## Code Style

- Follow standard Go conventions
- Run `golangci-lint` (config in `.golangci.yml`)
- Keep functions focused — max ~30 lines
- Use typed errors from `errors.go` for domain errors
- Agent package methods must hold `s.mu` and return JSON-serializable types
- Middleware must call `c.Next()` and handle `SaveIndex()`/`RestoreIndex()` for replay

## Tests

- Unit tests: `go test ./...` (no Chrome needed; integration tests live behind the `integration` build tag)
- Integration tests: `go test -tags integration -run TestIntegration ./...` (needs Chrome)
- Agent tests: `go test -tags integration ./agent/...`
- Always use `WithAllowPrivateIPs(true)` in tests that use `httptest.NewServer`

## Adding MCP Tools

1. Add handler in `cmd/scout/mcp.go`
2. Define input struct with `jsonschema` tags
3. Use `agent.Session` methods — don't access browse internals directly
4. Update tool count in README

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add shadow DOM traversal
fix: prevent hang in WaitVisible with timeout
docs: update MCP tool descriptions
test: add integration test for form discovery
```

## Reporting Issues

- Use GitHub Issues with the provided templates
- Include Go version, OS, Chrome version
- For bugs: minimal reproduction steps
