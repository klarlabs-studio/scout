package main

import (
	"os"
	"strings"
)

func configuredProxyServer() string {
	return strings.TrimSpace(os.Getenv("SCOUT_PROXY_SERVER"))
}
