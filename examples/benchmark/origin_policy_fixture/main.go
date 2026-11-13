// Command origin_policy_fixture serves an allowed page that probes a second,
// separately counted origin, plus control routes used by the benchmark runner.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
)

func main() {
	allowedAddr := flag.String("allowed-listen", "127.0.0.1:41745", "allowed page origin")
	deniedAddr := flag.String("denied-listen", "127.0.0.1:41746", "denied request origin")
	flag.Parse()
	var deniedHits atomic.Int64
	allowed := http.NewServeMux()
	allowed.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		denied := r.URL.Query().Get("denied")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Origin policy fixture</title><p id="ready">Ready</p><script>fetch(%q+'/hit',{mode:'no-cors'}).catch(()=>{});</script>`, strings.TrimSuffix(denied, "/"))
	})
	allowed.HandleFunc("/parent.html", func(w http.ResponseWriter, r *http.Request) {
		child := r.URL.Query().Get("child")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>OOPIF policy fixture</title><p id="parent-ready">Parent ready</p><iframe id="child-frame" src=%q></iframe>`, fmt.Sprintf("%q", strings.TrimSuffix(child, "/")+"/child.html"))
	})
	allowed.HandleFunc("/count", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"denied_origin_hits": deniedHits.Load()})
	})
	allowed.HandleFunc("/reset", func(w http.ResponseWriter, _ *http.Request) {
		deniedHits.Store(0)
		w.WriteHeader(http.StatusNoContent)
	})
	denied := http.NewServeMux()
	denied.HandleFunc("/child.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "")
		_, _ = fmt.Fprint(w, `<!doctype html><title>OOPIF child fixture</title><p id="child-ready">Child ready</p>`)
	})
	denied.HandleFunc("/hit", func(w http.ResponseWriter, _ *http.Request) {
		deniedHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	go func() { log.Fatal(http.ListenAndServe(*deniedAddr, denied)) }()
	log.Printf("origin policy fixture allowed=%s denied=%s", *allowedAddr, *deniedAddr)
	log.Fatal(http.ListenAndServe(*allowedAddr, allowed))
}
