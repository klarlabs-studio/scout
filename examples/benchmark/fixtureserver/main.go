// Command fixtureserver serves the deterministic browser benchmark pages for
// external comparison adapters.
package main

import (
	"flag"
	"log"
	"net/http"

	"go.klarlabs.de/scout/examples/benchmark/fixture"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:41731", "local address for the benchmark fixture server")
	processRecovery := flag.Bool("process-recovery", false, "serve the process-kill mutation recovery fixture")
	idempotencyOnly := flag.Bool("idempotency-only", false, "serve the caller idempotency-key fixture")
	flag.Parse()

	log.Printf("benchmark fixtures available at http://%s", *addr)
	var handler http.Handler
	if *processRecovery || *idempotencyOnly {
		handler = fixture.HandlerWithProcessRecovery()
	} else {
		handler, _, _ = fixture.HandlerWithLateActionSignals()
	}
	log.Fatal(http.ListenAndServe(*addr, handler))
}
