package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateUploadPath resolves filePath and refuses to upload something that
// doesn't exist, isn't a regular file, or names well-known credential material.
// A file upload driven by (possibly page-injected) instructions must not be
// coaxed into exfiltrating local secrets like an SSH key. It returns the
// resolved absolute path to hand to the browser.
func validateUploadPath(filePath string) (string, error) {
	if strings.TrimSpace(filePath) == "" {
		return "", fmt.Errorf("upload_file: empty file path")
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("upload_file: %w", err)
	}
	// Resolve symlinks so a link can't redirect the upload at a secret.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("upload_file: cannot access %q: %w", filePath, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("upload_file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("upload_file: %q is not a regular file", filePath)
	}
	if reason, blocked := sensitiveUploadReason(resolved); blocked {
		return "", fmt.Errorf("upload_file: refusing to upload %q (%s)", filePath, reason)
	}
	return resolved, nil
}

// sensitiveUploadReason reports whether an absolute path names well-known
// credential material that must never be uploaded, and why. Matching is on the
// resolved path's directory segments and base name, case-insensitively. It is
// intentionally a focused denylist of unambiguous secrets (private keys, cloud
// credentials, system secret files) — things never legitimately uploaded via a
// browser form.
func sensitiveUploadReason(absPath string) (string, bool) {
	lower := strings.ToLower(filepath.ToSlash(absPath))
	base := filepath.Base(lower)
	segments := strings.Split(lower, "/")

	sensitiveDirs := map[string]struct{}{
		".ssh": {}, ".gnupg": {}, ".aws": {}, ".azure": {},
		".kube": {}, ".docker": {}, "gcloud": {},
	}
	for _, seg := range segments {
		if _, ok := sensitiveDirs[seg]; ok {
			return "credential directory", true
		}
	}

	sensitiveBases := map[string]struct{}{
		"id_rsa": {}, "id_dsa": {}, "id_ecdsa": {}, "id_ed25519": {},
		".netrc": {}, ".pgpass": {},
	}
	if _, ok := sensitiveBases[base]; ok {
		return "credential file", true
	}

	switch lower {
	case "/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/master.passwd":
		return "system secret file", true
	}
	return "", false
}
