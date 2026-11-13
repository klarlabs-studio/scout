package browse

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func writeFile(path string, data []byte) error {
	return WritePrivateFile(path, data)
}

// WritePrivateFile atomically writes data to path with owner-only permissions.
// A sibling temporary file ensures that an existing symlink at path is
// replaced instead of followed, and that readers never observe partial data.
func WritePrivateFile(path string, data []byte) (retErr error) {
	if hasParentTraversal(path) {
		return fmt.Errorf("browse: path traversal detected in %q", path)
	}
	cleaned := filepath.Clean(path)
	dir := filepath.Dir(cleaned)
	file, err := os.CreateTemp(dir, ".scout-write-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	committed := false
	closed := false
	defer func() {
		if !closed {
			retErr = errors.Join(retErr, file.Close())
		}
		if !committed {
			if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("browse: remove temporary file %q: %w", tempPath, err))
			}
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	n, err := file.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("browse: short write to %q", path)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	if err := os.Rename(tempPath, cleaned); err != nil {
		return err
	}
	committed = true
	return nil
}

func hasParentTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
