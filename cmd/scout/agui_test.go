package main

import (
	"strings"
	"testing"
)

func TestServeUIOriginConfigValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		env  *string
		args []string
		want string
	}{
		{name: "empty environment", env: stringPointer(""), want: "SCOUT_UI_ALLOWED_ORIGINS is set but empty"},
		{name: "empty flag", args: []string{"--provider=ollama", "--allowed-origins="}, want: "--allowed-origins is set but empty"},
		{name: "unknown flag", args: []string{"--provider=ollama", "--allow-origin=*"}, want: "unknown ui serve flag"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.env == nil {
				t.Setenv("SCOUT_UI_ALLOWED_ORIGINS", "https://allowed.example")
			} else {
				t.Setenv("SCOUT_UI_ALLOWED_ORIGINS", *test.env)
			}
			err := runServeUI(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runServeUI error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConfiguredUIOriginsFlagOverridesEnvironment(t *testing.T) {
	flags := parseFlags([]string{"--allowed-origins=https://flag.example"})
	origins, err := configuredUIOrigins(flags, func(string) (string, bool) { return "", true })
	if err != nil {
		t.Fatal(err)
	}
	if len(origins) != 1 || origins[0] != "https://flag.example" {
		t.Fatalf("configured origins = %v", origins)
	}
}

func stringPointer(value string) *string { return &value }
