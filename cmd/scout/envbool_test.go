package main

import "testing"

func TestEnvBool(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"unset", "", false},
		{"true literal", "true", true},
		{"1", "1", true},
		{"0", "0", false},
		{"false", "false", false},
		{"FALSE upper", "FALSE", false},
		{"garbage", "yesplease", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SCOUT_TEST_ENV_BOOL", tc.val)
			if tc.val == "" {
				// Setenv cannot unset; emulate by using a unique unset key.
				if got := envBool("SCOUT_DEFINITELY_UNSET_KEY_QQQ"); got != false {
					t.Errorf("unset env should yield false, got %v", got)
				}
				return
			}
			if got := envBool("SCOUT_TEST_ENV_BOOL"); got != tc.want {
				t.Errorf("envBool(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
