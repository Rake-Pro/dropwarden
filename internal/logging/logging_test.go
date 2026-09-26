package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotateAtStartupKeepsPreviousGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dropwarden.log")
	old := bytes.Repeat([]byte("x"), 200)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := openRotating(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.f.Close()
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, old) {
		t.Fatalf("oversized log must be rotated to .1, got %d bytes", len(b))
	}
	if fi, _ := os.Stat(path); fi.Size() != 0 {
		t.Fatalf("fresh log must start empty, got %d bytes", fi.Size())
	}
}

func TestRotateWhileRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dropwarden.log")
	if err := os.WriteFile(path+".1", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := openRotating(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.f.Close() }()
	first := bytes.Repeat([]byte("a"), 80)
	second := bytes.Repeat([]byte("b"), 40)
	if _, err := r.Write(first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write(second); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, first) {
		t.Fatalf(".1 must hold the rotated generation (overwriting the stale one), got %q", b)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, second) {
		t.Fatalf("live log must hold only post-rotation writes, got %q", b)
	}
}
