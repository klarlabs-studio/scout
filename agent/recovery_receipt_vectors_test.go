package agent

import (
	"encoding/json"
	"os"
	"testing"
)

func TestOriginPolicySharedVectors(t *testing.T) {
	data, err := os.ReadFile("../docs/schemas/scout-origin-policy-v1-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name            string   `json:"name"`
		Origins         []string `json:"origins"`
		AllowPrivateIPs bool     `json:"allow_private_ips"`
		Canonical       []string `json:"canonical_origins"`
		PolicyID        string   `json:"policy_id"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			gotID := taskOriginPolicyID(vector.Origins, vector.AllowPrivateIPs)
			if gotID != vector.PolicyID {
				t.Fatalf("policy ID = %q, want %q", gotID, vector.PolicyID)
			}
			gotSet, err := normalizeTaskOriginSet(vector.Origins)
			if err != nil {
				t.Fatal(err)
			}
			gotOrigins := sortedTaskOrigins(gotSet)
			if len(gotOrigins) != len(vector.Canonical) {
				t.Fatalf("canonical origins = %#v, want %#v", gotOrigins, vector.Canonical)
			}
			for i := range gotOrigins {
				if gotOrigins[i] != vector.Canonical[i] {
					t.Fatalf("canonical origins = %#v, want %#v", gotOrigins, vector.Canonical)
				}
			}
		})
	}
}
