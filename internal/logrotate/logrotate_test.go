package logrotate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapFileKeepsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("line of cloudflared output number xxxx\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	original := int64(b.Len())

	if err := CapFile(path, 1000); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1000 {
		t.Fatalf("size %d, want <= 1000", info.Size())
	}
	if info.Size() >= original {
		t.Fatalf("file was not shrunk (size %d, original %d)", info.Size(), original)
	}
	data, _ := os.ReadFile(path)
	if len(data) > 0 && data[0] == '\n' {
		t.Fatal("kept content should start on a clean line, not a partial one")
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("kept content should end with the last full line")
	}
}

func TestCapFileNoopUnderCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.log")
	if err := os.WriteFile(path, []byte("tiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CapFile(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "tiny\n" {
		t.Fatalf("content changed: %q", data)
	}
	// Missing file is a no-op, not an error.
	if err := CapFile(filepath.Join(t.TempDir(), "nope.log"), 1<<20); err != nil {
		t.Fatalf("missing file should be a no-op: %v", err)
	}
}

func TestCapDirOnlyLogFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 5000)
	for _, name := range []string{"a.log", "b.log", "keep.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shrunk, err := CapDir(dir, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if shrunk != 2 {
		t.Fatalf("shrunk = %d, want 2 (.log only)", shrunk)
	}
	if info, _ := os.Stat(filepath.Join(dir, "keep.jsonl")); info.Size() != int64(len(big)) {
		t.Fatal(".jsonl file should be untouched by the janitor")
	}
}
