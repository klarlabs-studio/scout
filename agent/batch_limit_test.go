package agent

import (
	"strings"
	"testing"
)

func TestExecuteBatchRejectsMoreThan100Actions(t *testing.T) {
	s := &Session{}
	_, err := s.ExecuteBatch(make([]BatchAction, 101))
	if err == nil || !strings.Contains(err.Error(), "at most 100 actions") {
		t.Fatalf("expected batch size limit error, got %v", err)
	}
}

func TestExecuteBatchRejectsOversizedInputBeforeBrowserAccess(t *testing.T) {
	s := &Session{}
	_, err := s.ExecuteBatch([]BatchAction{{Action: "type", Selector: "#search", Value: strings.Repeat("x", MaxBatchInputBytes)}})
	if err == nil || !strings.Contains(err.Error(), "input exceeds") {
		t.Fatalf("expected batch input size error, got %v", err)
	}
}
