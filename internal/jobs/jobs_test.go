package jobs

import (
	"context"
	"errors"
	"net"
	"net/http"
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
