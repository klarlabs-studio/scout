package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSensitiveUploadReason(t *testing.T) {
	blocked := []string{
		"/home/user/.ssh/id_rsa",
		"/Users/x/.aws/credentials",
		"/home/user/.config/gcloud/application_default_credentials.json",
		"/home/user/.gnupg/secring.gpg",
		"/tmp/id_ed25519",
		"/home/user/.netrc",
		"/etc/shadow",
		"/etc/sudoers",
	}
	for _, p := range blocked {
		if _, ok := sensitiveUploadReason(p); !ok {
			t.Errorf("sensitiveUploadReason(%q) = allowed, want blocked", p)
		}
	}

	allowed := []string{
		"/home/user/Documents/resume.pdf",
		"/Users/x/Downloads/photo.png",
		"/var/data/report.csv",
		"/tmp/upload.txt",
	}
	for _, p := range allowed {
		if reason, ok := sensitiveUploadReason(p); ok {
			t.Errorf("sensitiveUploadReason(%q) = blocked (%s), want allowed", p, reason)
		}
	}
}

func TestValidateUploadPath(t *testing.T) {
	dir := t.TempDir()

	// A normal regular file resolves and is allowed.
	ok := filepath.Join(dir, "upload.txt")
	if err := os.WriteFile(ok, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := validateUploadPath(ok)
	if err != nil {
		t.Fatalf("validateUploadPath(regular file) error: %v", err)
	}
	if filepath.Base(resolved) != "upload.txt" {
		t.Errorf("resolved base = %q, want upload.txt", filepath.Base(resolved))
	}

	// Empty, missing, and directory paths are rejected.
	if _, err := validateUploadPath(""); err == nil {
		t.Error("empty path should error")
	}
	if _, err := validateUploadPath(filepath.Join(dir, "nope.txt")); err == nil {
		t.Error("missing file should error")
	}
	if _, err := validateUploadPath(dir); err == nil {
		t.Error("directory should error (not a regular file)")
	}

	// A real file under a credential directory is refused.
	sshDir := filepath.Join(dir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(sshDir, "some_key")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateUploadPath(key); err == nil {
		t.Error("file under .ssh should be refused")
	}

	// A real file whose base name is a private key is refused.
	idrsa := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(idrsa, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateUploadPath(idrsa); err == nil {
		t.Error("file named id_rsa should be refused")
	}
}
