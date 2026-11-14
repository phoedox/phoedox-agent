package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	if _, err := Load(dir); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty: %v", err)
	}
	want := State{Instance: "https://x.example", AgentID: "a1", Token: "phxa_x", Project: "lab"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || got != want {
		t.Fatalf("%v %+v", err, got)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, File))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode())
		}
	}
}

func TestDone(t *testing.T) {
	dir := t.TempDir()
	if Done(dir, "j1") {
		t.Fatal("nothing done yet")
	}
	for i := range 510 {
		if err := MarkDone(dir, fmt.Sprint("j", i)); err != nil {
			t.Fatal(err)
		}
	}
	if !Done(dir, "j509") || Done(dir, "j0") {
		t.Fatal("keeps the newest 500")
	}
}
