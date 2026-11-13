package docker

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
)

func TestContainers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" || r.URL.Query().Get("all") != "1" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"Names":["/web"],"Image":"nginx:1.29","State":"running","Ports":[
			{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"IP":"::","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"PrivatePort":443,"Type":"tcp"}]},
			{"Names":["/db"],"Image":"postgres:18","State":"exited","Ports":[]}]`))
	})}
	go srv.Serve(l)
	defer srv.Close()

	got, err := Containers(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "db" || got[1].Name != "web" || got[1].Ports != "8080:80/tcp" || got[0].State != "exited" {
		t.Fatalf("%+v", got)
	}
	if _, err := Containers(context.Background(), filepath.Join(t.TempDir(), "none.sock")); err == nil {
		t.Fatal("missing socket must fail")
	}
}
