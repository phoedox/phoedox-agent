// Package homeassistant reads a Home Assistant in the agent's network
// (devices with their area, entity states) and switches entities for signed
// ha.call jobs. The long-lived token is read from a local file and never
// leaves this host.
package homeassistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// registryTemplate asks for all devices with an area in one request (the
// same template as the Home Assistant plugin, which parses the result).
const registryTemplate = `{% set ns = namespace(out=[]) %}{% for a in areas() %}{% for d in area_devices(a) %}` +
	`{% set ns.out = ns.out + [{'area': area_name(a), 'id': d, 'name': device_attr(d, 'name_by_user') or device_attr(d, 'name'),` +
	` 'manufacturer': device_attr(d, 'manufacturer'), 'model': device_attr(d, 'model'), 'entities': device_entities(d) | list}] %}{% endfor %}{% endfor %}{{ ns.out | tojson }}`

// DeviceInterval is how often the device list is sent even if unchanged.
const DeviceInterval = 6 * time.Hour

// maxStates caps the states of one report.
const maxStates = 5000

// Report is the homeassistant part of a snapshot.
type Report struct {
	Devices json.RawMessage `json:"devices,omitempty"`
	States  []State         `json:"states,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// State is the state of one entity.
type State struct {
	EntityID string `json:"entity_id"`
	State    string `json:"state"`
	Unit     string `json:"unit,omitempty"`
}

// Client talks to one Home Assistant.
type Client struct {
	URL       string
	TokenFile string
	HTTP      *http.Client

	mu       sync.Mutex
	entities map[string]bool // entities of devices with an area
	sum      [32]byte
	sentAt   time.Time
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("home assistant token: %w", err)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("home assistant answered HTTP %d", res.StatusCode)
	}
	return b, nil
}

// Read returns devices (when they changed or DeviceInterval passed) and the
// states of the devices' entities. Errors end up in Report.Error: reading
// may fail without stopping the agent.
func (c *Client) Read(ctx context.Context, now time.Time) *Report {
	r := &Report{}
	devices, err := c.do(ctx, http.MethodPost, "/api/template", map[string]string{"template": registryTemplate})
	if err != nil {
		r.Error = err.Error()
		return r
	}
	var list []struct {
		Entities []string `json:"entities"`
	}
	if err := json.Unmarshal(devices, &list); err != nil {
		r.Error = "unexpected device list from home assistant"
		return r
	}
	c.mu.Lock()
	c.entities = map[string]bool{}
	for _, d := range list {
		for _, e := range d.Entities {
			c.entities[e] = true
		}
	}
	if sum := sha256.Sum256(devices); sum != c.sum || now.Sub(c.sentAt) >= DeviceInterval {
		r.Devices, c.sum, c.sentAt = devices, sum, now
	}
	known := c.entities
	c.mu.Unlock()

	body, err := c.do(ctx, http.MethodGet, "/api/states", nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	var states []struct {
		EntityID   string         `json:"entity_id"`
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		r.Error = "unexpected states from home assistant"
		return r
	}
	for _, s := range states {
		if !known[s.EntityID] || len(r.States) >= maxStates {
			continue
		}
		unit, _ := s.Attributes["unit_of_measurement"].(string)
		r.States = append(r.States, State{EntityID: s.EntityID, State: s.State, Unit: unit})
	}
	return r
}

// Call runs a switch service on an entity (turn_on, turn_off, toggle). The
// caller has checked the signed job with policy.Check.
func (c *Client) Call(ctx context.Context, entity, service string) (string, error) {
	domain, _, ok := strings.Cut(entity, ".")
	if !ok || domain == "" {
		return "", errors.New("entity must look like domain.name")
	}
	if _, err := c.do(ctx, http.MethodPost, "/api/services/"+domain+"/"+service, map[string]string{"entity_id": entity}); err != nil {
		return "", err
	}
	body, err := c.do(ctx, http.MethodGet, "/api/states/"+entity, nil)
	if err != nil {
		return "done", nil
	}
	var s struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(body, &s)
	return entity + " is " + s.State, nil
}
