//go:build integration

package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestIntegrationMetaASelectsAllOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Meta+A text selection semantics are platform-specific")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><input id="field"></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Type("#field", "Scout keyboard dogfood"); err != nil {
		t.Fatal(err)
	}
	result, err := s.PressKeyWithPostcondition("Meta+A", agent.Postcondition{ValueSelector: "#field", Value: "Scout keyboard dogfood"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified {
		t.Fatalf("Meta+A postcondition = %+v", result)
	}
	selection, err := s.EvalContext(context.Background(), `(()=>{const e=document.querySelector('#field');return {active:document.activeElement===e,start:e.selectionStart,end:e.selectionEnd,length:e.value.length}})()`)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := selection.(map[string]any)
	if !ok || state["active"] != true || state["start"] != float64(0) || state["end"] != state["length"] {
		t.Fatalf("Meta+A did not select the focused input contents: %#v", selection)
	}
	cleared, err := s.PressKeyWithPostcondition("Backspace", agent.Postcondition{ValueSelector: "#field", Value: ""})
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Verified {
		t.Fatalf("Backspace after Meta+A = %+v", cleared)
	}
}
