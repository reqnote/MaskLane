package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProfileDataDirectory(t *testing.T) {
	local := t.TempDir()
	want := filepath.Join(local, "MaskLane")
	checkDirectory := func() {
		t.Helper()
		if got, err := profileDataDirectory(local); err != nil || got != want {
			t.Fatalf("profileDataDirectory = %q, %v, want %q", got, err, want)
		}
	}
	checkDirectory()

	legacy := filepath.Join(local, "MaskingGateway")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureCA(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "mappings.json"), []byte(`{"revision":1,"rules":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	read := func(dir, name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	files := map[string][]byte{}
	for _, name := range []string{"mappings.json", "ca.pem", "ca-key.pem"} {
		files[name] = read(legacy, name)
	}
	lock, err := lockDataDirectory(filepath.Join(legacy, "gateway.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = profileDataDirectory(local)
	if closeErr := lock.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil {
		t.Fatal("migrated while the previous application held its lock")
	}

	checkDirectory()
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous directory still present: %v", err)
	}
	if _, _, err := ensureCA(want); err != nil {
		t.Fatal(err)
	}
	for name, original := range files {
		if !bytes.Equal(read(want, name), original) {
			t.Errorf("%s changed during migration", name)
		}
	}
	checkDirectory()
}
