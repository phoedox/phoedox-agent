package collect

import (
	"bufio"
	"io"
	"sort"
	"strings"
)

// Package is an installed system package.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// maxPackages caps the list (a full desktop has a few thousand).
const maxPackages = 10000

// parseDpkg reads /var/lib/dpkg/status: stanzas with Package, Status and Version.
func parseDpkg(r io.Reader) []Package {
	var out []Package
	var cur Package
	installed := false
	flush := func() {
		if cur.Name != "" && installed && len(out) < maxPackages {
			out = append(out, cur)
		}
		cur, installed = Package{}, false
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "Package: "):
			cur.Name = strings.TrimPrefix(line, "Package: ")
		case strings.HasPrefix(line, "Version: "):
			cur.Version = strings.TrimPrefix(line, "Version: ")
		case strings.HasPrefix(line, "Status: "):
			installed = strings.HasSuffix(line, " installed")
		}
	}
	flush()
	return sorted(out)
}

// parseApk reads /lib/apk/db/installed: stanzas with P: (name) and V: (version).
func parseApk(r io.Reader) []Package {
	var out []Package
	var cur Package
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.Name != "" && len(out) < maxPackages {
				out = append(out, cur)
			}
			cur = Package{}
		case strings.HasPrefix(line, "P:"):
			cur.Name = line[2:]
		case strings.HasPrefix(line, "V:"):
			cur.Version = line[2:]
		}
	}
	if cur.Name != "" && len(out) < maxPackages {
		out = append(out, cur)
	}
	return sorted(out)
}

// parseRpmQuery reads `rpm -qa --qf '%{NAME}\t%{VERSION}-%{RELEASE}\n'`.
func parseRpmQuery(r io.Reader) []Package {
	var out []Package
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, version, ok := strings.Cut(sc.Text(), "\t")
		if ok && name != "" && name != "gpg-pubkey" && len(out) < maxPackages {
			out = append(out, Package{Name: name, Version: version})
		}
	}
	return sorted(out)
}

func sorted(p []Package) []Package {
	sort.Slice(p, func(i, j int) bool { return p[i].Name < p[j].Name })
	return p
}
