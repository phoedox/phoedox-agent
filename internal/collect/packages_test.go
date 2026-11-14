package collect

import (
	"strings"
	"testing"
)

func TestParseDpkg(t *testing.T) {
	status := `Package: docker-ce
Status: install ok installed
Version: 5:28.1.1-1~debian.12~bookworm

Package: old-thing
Status: deinstall ok config-files
Version: 1.0

Package: bash
Status: install ok installed
Priority: required
Version: 5.2.15-2+b7
`
	got := parseDpkg(strings.NewReader(status))
	if len(got) != 2 || got[0].Name != "bash" || got[1].Version != "5:28.1.1-1~debian.12~bookworm" {
		t.Fatalf("%+v", got)
	}
}

func TestParseApk(t *testing.T) {
	got := parseApk(strings.NewReader("C:Q1abc=\nP:musl\nV:1.2.5-r0\n\nP:busybox\nV:1.36.1-r29\n"))
	if len(got) != 2 || got[0].Name != "busybox" || got[1].Version != "1.2.5-r0" {
		t.Fatalf("%+v", got)
	}
}

func TestParseRpm(t *testing.T) {
	got := parseRpmQuery(strings.NewReader("podman\t5.2.2-1.el9\ngpg-pubkey\tx\nbroken\n"))
	if len(got) != 1 || got[0].Name != "podman" {
		t.Fatalf("%+v", got)
	}
}
