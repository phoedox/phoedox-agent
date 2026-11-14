// Package docker talks to the local Docker API socket: it lists containers
// and, for approved jobs only, restarts one. A missing socket yields no
// containers.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/phoedox/phoedox-agent/internal/collect"
)

// DefaultSocket is where Docker listens on Linux.
const DefaultSocket = "/var/run/docker.sock"

// Socket is DOCKER_HOST when it names a unix socket (also Podman's), else DefaultSocket.
func Socket() string {
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && h != "" {
		return h
	}
	return DefaultSocket
}

type apiContainer struct {
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

func client(socket string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
}

// Restart restarts a container (the only write call of this package; the
// caller has checked the job with policy.Check).
func Restart(ctx context.Context, socket, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(name)+"/restart?t=10", nil)
	if err != nil {
		return err
	}
	res, err := client(socket, 90*time.Second).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		return fmt.Errorf("docker: restart %s: %s", name, res.Status)
	}
	return nil
}

func get(ctx context.Context, socket, path string, out any) error {
	c := client(socket, 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("docker: %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Containers returns all containers (running or not) of the daemon at socket.
func Containers(ctx context.Context, socket string) ([]collect.Container, error) {
	var list []apiContainer
	if err := get(ctx, socket, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}
	return convert(list), nil
}

// Version returns the engine version.
func Version(ctx context.Context, socket string) (string, error) {
	var v struct {
		Version string `json:"Version"`
	}
	err := get(ctx, socket, "/version", &v)
	return v.Version, err
}

func convert(list []apiContainer) []collect.Container {
	out := make([]collect.Container, 0, len(list))
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		seen := map[string]bool{}
		var ports []string
		for _, p := range c.Ports {
			if p.PublicPort == 0 {
				continue
			}
			s := fmt.Sprintf("%d:%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			if !seen[s] { // IPv4 and IPv6 bindings repeat the same port
				seen[s] = true
				ports = append(ports, s)
			}
		}
		sort.Strings(ports)
		out = append(out, collect.Container{Name: name, Image: c.Image, State: c.State, Ports: strings.Join(ports, ", "),
			Project: c.Labels["com.docker.compose.project"], Service: c.Labels["com.docker.compose.service"],
			ComposeDir: c.Labels["com.docker.compose.project.working_dir"]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
