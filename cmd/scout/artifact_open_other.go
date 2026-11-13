//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package main

import "os"

func openPrivateArtifact(path string) (*os.File, error) {
	return os.Open(path)
}
