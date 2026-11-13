package browse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeBase64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"valid", "aGVsbG8=", "hello", false},
		{"empty", "", "", false},
		{"padding", "YQ==", "a", false},
		{"invalid", "!!!invalid!!!", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeBase64(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("decodeBase64(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestWriteFile(t *testing.T) {
	t.Run("writes file successfully", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		data := []byte("hello world")

		err := writeFile(path, data)
		if err != nil {
			t.Fatalf("writeFile() error: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error: %v", err)
		}
		if string(got) != "hello world" {
			t.Errorf("file content = %q, want %q", got, "hello world")
		}
	})

	t.Run("blocks path traversal", func(t *testing.T) {
		err := writeFile("../../../etc/passwd", []byte("bad"))
		if err == nil {
			t.Error("expected error for path traversal")
		}
		if err != nil && !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("error should mention path traversal, got: %v", err)
		}
	})

	t.Run("allows dots inside a filename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "report..backup.txt")
		if err := writeFile(path, []byte("safe")); err != nil {
			t.Fatalf("writeFile() rejected a filename containing dots: %v", err)
		}
	})

	t.Run("replaces a symlink without following it", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.txt")
		link := filepath.Join(dir, "output.txt")
		if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := writeFile(link, []byte("new artifact")); err != nil {
			t.Fatalf("writeFile() error: %v", err)
		}
		gotTarget, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotTarget) != "keep" {
			t.Fatalf("symlink target was modified: %q", gotTarget)
		}
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("output path is still a symlink")
		}
		gotLink, err := os.ReadFile(link)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotLink) != "new artifact" {
			t.Fatalf("output content = %q", gotLink)
		}
	})

	t.Run("empty data", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.txt")

		err := writeFile(path, []byte{})
		if err != nil {
			t.Fatalf("writeFile() error: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error: %v", err)
		}
		if info.Size() != 0 {
			t.Errorf("file size = %d, want 0", info.Size())
		}
	})
}

func TestJsonQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "hello", `"hello"`},
		{"empty", "", `""`},
		{"quotes", `say "hi"`, `"say \"hi\""`},
		{"backslash", `a\b`, `"a\\b"`},
		{"newline", "line1\nline2", `"line1\nline2"`},
		{"tab", "a\tb", `"a\tb"`},
		{"unicode", "hello\u0000world", `"hello\u0000world"`},
		{"html special", "<script>", `"\u003cscript\u003e"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsonQuote(tt.in)
			if got != tt.want {
				t.Errorf("jsonQuote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
