package homeassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadAndCall(t *testing.T) {
	state := map[string]string{"switch.flur": "on"}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "no", 401)
			return
		}
		switch {
		case r.URL.Path == "/api/template":
			w.Write([]byte(`[{"id":"d1","name":"Plug","area":"Flur","entities":["switch.flur","sensor.flur_power"]}]`))
		case r.URL.Path == "/api/states":
			json.NewEncoder(w).Encode([]map[string]any{
				{"entity_id": "switch.flur", "state": state["switch.flur"], "attributes": map[string]any{}},
				{"entity_id": "sensor.flur_power", "state": "7.5", "attributes": map[string]any{"unit_of_measurement": "W"}},
				{"entity_id": "sun.sun", "state": "above_horizon", "attributes": map[string]any{}},
			})
		case strings.HasPrefix(r.URL.Path, "/api/services/"):
			calls = append(calls, r.URL.Path)
			state["switch.flur"] = "off"
			w.Write([]byte(`[]`))
		case strings.HasPrefix(r.URL.Path, "/api/states/"):
			json.NewEncoder(w).Encode(map[string]string{"state": state["switch.flur"]})
		}
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("secret-token\n"), 0o600)
	c := &Client{URL: srv.URL, TokenFile: tok}
	now := time.Now()

	r := c.Read(context.Background(), now)
	if r.Error != "" || len(r.Devices) == 0 || len(r.States) != 2 || r.States[1].Unit != "W" {
		t.Fatalf("first read: %+v", r)
	}
	// Unchanged devices are not sent again until DeviceInterval passed.
	if r := c.Read(context.Background(), now.Add(time.Minute)); len(r.Devices) != 0 || len(r.States) != 2 {
		t.Fatalf("second read: %+v", r)
	}
	if r := c.Read(context.Background(), now.Add(DeviceInterval)); len(r.Devices) == 0 {
		t.Fatal("devices not resent after the interval")
	}
	out, err := c.Call(context.Background(), "switch.flur", "turn_off")
	if err != nil || out != "switch.flur is off" || calls[0] != "/api/services/switch/turn_off" {
		t.Fatalf("call: %q %v %v", out, err, calls)
	}

	// Reading fails softly.
	bad := &Client{URL: srv.URL, TokenFile: filepath.Join(t.TempDir(), "missing")}
	if r := bad.Read(context.Background(), now); r.Error == "" {
		t.Fatal("missing token file must be reported")
	}
}
