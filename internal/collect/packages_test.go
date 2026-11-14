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

func TestSourcePackages(t *testing.T) {
	status := `Package: libssl3
Status: install ok installed
Source: openssl
Version: 3.0.15-1~deb12u1

Package: libc6
Status: install ok installed
Source: glibc (2.36-9+deb12u9)
Version: 2.36-9+deb12u9

Package: bash
Status: install ok installed
Source: bash
Version: 5.2.15-2+b7

Package: libsqlite3-0
Status: install ok installed
Source: sqlite3 (3.40.1-2)
Version: 3.40.1-2+b1
`
	got := parseDpkg(strings.NewReader(status))
	want := map[string][2]string{"libssl3": {"openssl", ""}, "libc6": {"glibc", ""}, "bash": {"", ""}, "libsqlite3-0": {"sqlite3", "3.40.1-2"}}
	for _, p := range got {
		if w := want[p.Name]; p.Source != w[0] || p.SourceVersion != w[1] {
			t.Errorf("%s: source %q %q, want %v", p.Name, p.Source, p.SourceVersion, w)
		}
	}
	apk := parseApk(strings.NewReader("P:libcrypto3\nV:3.3.2-r0\no:openssl\n\nP:musl\nV:1.2.5-r0\no:musl\n"))
	if apk[0].Name != "libcrypto3" || apk[0].Source != "openssl" || apk[1].Source != "" {
		t.Fatalf("apk: %+v", apk)
	}
}
