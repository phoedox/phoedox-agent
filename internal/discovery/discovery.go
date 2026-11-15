// Package discovery finds devices in the agent's network without scanning:
// it reads the host's ARP/neighbour table and asks once per round over mDNS
// (DNS-SD) which services announce themselves. It is off unless agent.yaml
// switches it on (discovery.enabled), and the instance drops the results
// for organizations that forbid discovery.
package discovery

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Config is the discovery section of agent.yaml.
type Config struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// MDNS asks the network for announced services (default true when enabled).
	MDNS *bool `json:"mdns,omitempty" yaml:"mdns"`
	// EveryMinutes is the time between rounds (default 30).
	EveryMinutes int `json:"everyMinutes,omitempty" yaml:"everyMinutes"`
}

// Device is a device seen in the network.
type Device struct {
	IP       string   `json:"ip"`
	MAC      string   `json:"mac,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Services []string `json:"services,omitempty"`
	Via      []string `json:"via"` // arp, mdns
}

// MaxDevices caps one round.
const MaxDevices = 2000

// Finder runs discovery rounds.
type Finder struct {
	Config Config
	// ARPFile is the neighbour table (Linux: /proc/net/arp).
	ARPFile string
	// Browse asks mDNS; tests replace it.
	Browse func(ctx context.Context, wait time.Duration) []Device

	mu   sync.Mutex
	last time.Time
}

// Read runs a round when the interval is over and returns nil otherwise.
func (f *Finder) Read(ctx context.Context, now time.Time) []Device {
	if !f.Config.Enabled {
		return nil
	}
	every := time.Duration(f.Config.EveryMinutes) * time.Minute
	if every <= 0 {
		every = 30 * time.Minute
	}
	f.mu.Lock()
	if !f.last.IsZero() && now.Sub(f.last) < every {
		f.mu.Unlock()
		return nil
	}
	f.last = now
	f.mu.Unlock()
	byIP := map[string]*Device{}
	add := func(d Device, via string) {
		if net.ParseIP(d.IP) == nil {
			return
		}
		x := byIP[d.IP]
		if x == nil {
			if len(byIP) >= MaxDevices {
				return
			}
			x = &Device{IP: d.IP}
			byIP[d.IP] = x
		}
		if d.MAC != "" {
			x.MAC = d.MAC
		}
		if d.Hostname != "" {
			x.Hostname = d.Hostname
		}
		for _, s := range d.Services {
			if !contains(x.Services, s) {
				x.Services = append(x.Services, s)
			}
		}
		if !contains(x.Via, via) {
			x.Via = append(x.Via, via)
		}
	}
	file := f.ARPFile
	if file == "" {
		file = "/proc/net/arp"
	}
	if r, err := os.Open(file); err == nil {
		for _, d := range ParseARP(r) {
			add(d, "arp")
		}
		r.Close()
	}
	if f.Config.MDNS == nil || *f.Config.MDNS {
		browse := f.Browse
		if browse == nil {
			browse = BrowseMDNS
		}
		for _, d := range browse(ctx, 3*time.Second) {
			add(d, "mdns")
		}
	}
	out := make([]Device, 0, len(byIP))
	for _, d := range byIP {
		sort.Strings(d.Services)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ParseARP reads /proc/net/arp: complete entries (flags 0x2) with a MAC.
func ParseARP(r io.Reader) []Device {
	var out []Device
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 6 || f[2] == "0x0" {
			continue
		}
		mac, err := net.ParseMAC(f[3])
		if err != nil || mac.String() == "00:00:00:00:00:00" {
			continue
		}
		out = append(out, Device{IP: f[0], MAC: mac.String()})
	}
	return out
}

var mdnsAddr = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// BrowseMDNS asks which services announce themselves (DNS-SD) and collects
// the answers for wait: host names, addresses and service types.
func BrowseMDNS(ctx context.Context, wait time.Duration) []Device {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil
	}
	defer conn.Close()
	ask := func(name string, t dnsmessage.Type) {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
		_ = b.StartQuestions()
		n, err := dnsmessage.NewName(name)
		if err != nil {
			return
		}
		_ = b.Question(dnsmessage.Question{Name: n, Type: t, Class: dnsmessage.ClassINET})
		if msg, err := b.Finish(); err == nil {
			_, _ = conn.WriteToUDP(msg, mdnsAddr)
		}
	}
	ask("_services._dns-sd._udp.local.", dnsmessage.TypePTR)
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	c := newCollector()
	asked := map[string]bool{}
	buf := make([]byte, 9000)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		for _, svc := range c.add(buf[:n], from.IP.String()) {
			if !asked[svc] && len(asked) < 64 {
				asked[svc] = true
				ask(svc, dnsmessage.TypePTR)
			}
		}
	}
	return c.devices()
}

// collector joins mDNS answers into devices.
type collector struct {
	hosts    map[string]string   // host name → IPv4
	services map[string][]string // host name → service types
	srv      map[string]string   // instance → host name
	byIP     map[string]*Device
}

func newCollector() *collector {
	return &collector{hosts: map[string]string{}, services: map[string][]string{}, srv: map[string]string{}, byIP: map[string]*Device{}}
}

// add reads one mDNS message; it returns service types worth asking for.
func (c *collector) add(msg []byte, from string) []string {
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return nil
	}
	_ = p.SkipAllQuestions()
	var types []string
	var rrs []dnsmessage.Resource
	for _, section := range []func() ([]dnsmessage.Resource, error){p.AllAnswers, p.AllAuthorities, p.AllAdditionals} {
		r, err := section()
		if err != nil {
			break
		}
		rrs = append(rrs, r...)
	}
	instanceType := map[string]string{}
	for _, r := range rrs {
		name := r.Header.Name.String()
		switch b := r.Body.(type) {
		case *dnsmessage.PTRResource:
			target := b.PTR.String()
			if name == "_services._dns-sd._udp.local." {
				types = append(types, target)
			} else {
				instanceType[target] = strings.TrimSuffix(name, ".local.")
			}
		case *dnsmessage.SRVResource:
			c.srv[name] = b.Target.String()
		case *dnsmessage.AResource:
			c.hosts[name] = net.IP(b.A[:]).String()
		}
	}
	for inst, typ := range instanceType {
		if host := c.srv[inst]; host != "" {
			c.services[host] = append(c.services[host], typ)
		} else {
			// Without SRV the sender is the host.
			d := c.device(from)
			if !contains(d.Services, typ) {
				d.Services = append(d.Services, typ)
			}
		}
	}
	for host, ip := range c.hosts {
		d := c.device(ip)
		d.Hostname = strings.TrimSuffix(strings.TrimSuffix(host, "."), ".local")
		for _, s := range c.services[host] {
			if !contains(d.Services, s) {
				d.Services = append(d.Services, s)
			}
		}
	}
	return types
}

func (c *collector) device(ip string) *Device {
	d := c.byIP[ip]
	if d == nil {
		d = &Device{IP: ip}
		c.byIP[ip] = d
	}
	return d
}

func (c *collector) devices() []Device {
	out := make([]Device, 0, len(c.byIP))
	for _, d := range c.byIP {
		out = append(out, *d)
	}
	return out
}
