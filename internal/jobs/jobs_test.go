package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phoedox/phoedox-agent/internal/policy"
)

type call struct {
	name string
	args []string
}

func fake(fail string) (*Runner, *[]call) {
	var calls []call
	r := &Runner{
		LookPath: func(n string) (string, error) {
			if n == "apt-get" {
				return "/usr/bin/apt-get", nil
			}
			return "", errors.New("missing")
		},
		Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, call{name, args})
			if fail != "" && strings.Contains(strings.Join(args, " "), fail) {
				return []byte("boom"), errors.New("exit 1")
			}
			return []byte("ok\n"), nil
		},
	}
	return r, &calls
}

func TestCompose(t *testing.T) {
	r, calls := fake("")
	res := r.Run(context.Background(), policy.Job{Action: policy.ActStackRedeploy, Args: []string{"cloud", "/opt/cloud"}})
	if !res.OK || len(*calls) != 2 || strings.Join((*calls)[1].args, " ") != "compose --project-name cloud --project-directory /opt/cloud up -d" {
		t.Fatalf("%+v %+v", res, *calls)
	}
	r, calls = fake("")
	res = r.Run(context.Background(), policy.Job{Action: policy.ActContainerUpdate, Args: []string{"cloud", "/opt/cloud", "app"}})
	if !res.OK || (*calls)[0].args[len((*calls)[0].args)-1] != "app" {
		t.Fatalf("%+v %+v", res, *calls)
	}
	for _, args := range [][]string{{"cloud", "relative"}, {"cl oud", "/opt"}, {"cloud", "/opt/../etc"}} {
		if res := r.Run(context.Background(), policy.Job{Action: policy.ActStackRedeploy, Args: args}); res.OK {
			t.Errorf("%v must fail", args)
		}
	}
	r, _ = fake("pull")
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActStackRedeploy, Args: []string{"cloud", "/opt/cloud"}}); res.OK || !strings.Contains(res.Output, "boom") {
		t.Fatalf("failed pull: %+v", res)
	}
}

func TestSystemUpdateAndExec(t *testing.T) {
	r, calls := fake("")
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActSystemUpdate}); !res.OK || (*calls)[0].name != "apt-get" || len(*calls) != 2 {
		t.Fatalf("%+v %+v", res, *calls)
	}
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActExec, Args: []string{"backup.sh"}}); res.OK {
		t.Fatal("relative exec must fail")
	}
	if res := r.Run(context.Background(), policy.Job{Action: "rm"}); res.OK {
		t.Fatal("unknown action")
	}
}

func TestRestart(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	var got string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(l)
	defer srv.Close()
	r := &Runner{DockerSocket: sock}
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActContainerRestart, Args: []string{"web"}}); !res.OK || got != "POST /containers/web/restart" {
		t.Fatalf("%+v %q", res, got)
	}
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActContainerRestart, Args: []string{"../x"}}); res.OK {
		t.Fatal("bad name")
	}
}

func TestAgentUpdate(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "phoedox-agent")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := []byte("new binary")
	sum := sha256.Sum256(newBin)
	good := hex.EncodeToString(sum[:])
	var asked string
	r := &Runner{Executable: func() (string, error) { return exe, nil },
		Download: func(_ context.Context, path string) (io.ReadCloser, error) {
			asked = path
			return io.NopCloser(bytes.NewReader(newBin)), nil
		}}
	path := "/api/v1/agent/releases/v0.3.0/phoedox-agent-linux-amd64"
	job := func(args ...string) policy.Job { return policy.Job{Action: policy.ActAgentUpdate, Args: args} }

	for _, args := range [][]string{{"https://evil.example/agent", good}, {path, "abc"}, {"/api/v1/agent/releases/../../x", good}} {
		if res := r.Run(context.Background(), job(args...)); res.OK {
			t.Fatalf("accepted %v", args)
		}
	}
	bad := strings.Repeat("0", 64)
	if res := r.Run(context.Background(), job(path, bad)); res.OK || !strings.Contains(res.Output, "checksum mismatch") {
		t.Fatalf("wrong checksum: %+v", res)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" || r.Restart {
		t.Fatal("binary replaced despite wrong checksum")
	}
	res := r.Run(context.Background(), job(path, good))
	if !res.OK || !r.Restart || asked != path {
		t.Fatalf("update: %+v restart=%v asked=%s", res, r.Restart, asked)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("binary: %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old" {
		t.Fatalf("old binary kept: %q", b)
	}
}
