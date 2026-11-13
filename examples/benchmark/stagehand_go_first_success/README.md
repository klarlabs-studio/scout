# Stagehand Go deterministic first-success arm

This adapter exercises Stagehand's documented direct page API without a model
provider or semantic action. It starts a local fixture and local Chrome,
initializes the Stagehand worker with `Model` unset, clicks the fixture button,
and checks the resulting page text.

Pinned dependency: `github.com/browserbase/stagehand/packages/sdk-go/v4`
v4.3.0. The module checksum recorded by Go is in `go.sum`. Run from this
directory with:

```sh
go run .
```

The process emits one JSON report. `time_to_verified_ms` covers process start,
local Chrome startup, worker initialization, navigation, click, and direct DOM
verification. It excludes module downloads and Chrome installation. One local
run on Go 1.27.3 completed successfully in 2188 ms. This is a setup/path
feasibility check, not a comparative latency result; matched participant runs
must use the same host, browser build, and timing boundary.

The check establishes that this direct deterministic path does not require a
provider API key or model call in the pinned release. It does require the
Stagehand Go SDK worker runtime and a local Chrome installation. Semantic
`Act`, `Observe`, and `Extract` are outside this arm.
