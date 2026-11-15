// Package snmp polls switches, routers and access points in the agent's
// network over SNMP (v2c or v3): system data, interfaces, LLDP neighbours
// and the bridge MAC table. It only reads. Targets and credentials come
// from agent.yaml; communities and keys are read from local files and never
// leave this host.
package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Limits keep one report small.
const (
	MaxInterfaces = 1024
	MaxNeighbors  = 512
	MaxFDB        = 8192
	DefaultEvery  = 15 * time.Minute
)

// Target is one device in agent.yaml (snmp.targets).
type Target struct {
	Host string `json:"host" yaml:"host"`
	Port uint16 `json:"port,omitempty" yaml:"port"`
	// Name overrides the device's sysName for the entry.
	Name string `json:"name,omitempty" yaml:"name"`
	// Version is "2c" (default) or "3".
	Version string `json:"version,omitempty" yaml:"version"`
	// CommunityFile holds the v2c community (absolute path).
	CommunityFile string `json:"communityFile,omitempty" yaml:"communityFile"`
	// V3 user and key files.
	User         string `json:"user,omitempty" yaml:"user"`
	AuthProtocol string `json:"authProtocol,omitempty" yaml:"authProtocol"` // SHA (default), SHA256, SHA512, MD5
	AuthKeyFile  string `json:"authKeyFile,omitempty" yaml:"authKeyFile"`
	PrivProtocol string `json:"privProtocol,omitempty" yaml:"privProtocol"` // AES (default), AES256, DES, none
	PrivKeyFile  string `json:"privKeyFile,omitempty" yaml:"privKeyFile"`
}

// Config is the snmp section of agent.yaml.
type Config struct {
	Targets []Target `json:"targets" yaml:"targets"`
	// EveryMinutes is the time between polls (default 15).
	EveryMinutes int `json:"everyMinutes,omitempty" yaml:"everyMinutes"`
}

// Validate checks targets: hosts, versions and absolute key files.
func (c Config) Validate() error {
	for i, t := range c.Targets {
		if strings.TrimSpace(t.Host) == "" {
			return fmt.Errorf("snmp.targets[%d]: host is missing", i)
		}
		switch t.Version {
		case "", "2c":
			if !absFile(t.CommunityFile) {
				return fmt.Errorf("snmp.targets[%d]: communityFile must be an absolute path", i)
			}
		case "3":
			if t.User == "" {
				return fmt.Errorf("snmp.targets[%d]: user is missing", i)
			}
			if t.AuthKeyFile != "" && !absFile(t.AuthKeyFile) || t.PrivKeyFile != "" && !absFile(t.PrivKeyFile) {
				return fmt.Errorf("snmp.targets[%d]: key files must be absolute paths", i)
			}
		default:
			return fmt.Errorf("snmp.targets[%d]: version must be 2c or 3", i)
		}
	}
	return nil
}

func absFile(p string) bool { return filepath.IsAbs(p) }

// Device is what one poll found.
type Device struct {
	Host       string      `json:"host"`
	Name       string      `json:"name,omitempty"`
	SysName    string      `json:"sysName,omitempty"`
	Descr      string      `json:"descr,omitempty"`
	ObjectID   string      `json:"objectId,omitempty"`
	Contact    string      `json:"contact,omitempty"`
	Location   string      `json:"location,omitempty"`
	UptimeSec  int64       `json:"uptimeSec,omitempty"`
	Services   int         `json:"services,omitempty"`
	Interfaces []Interface `json:"interfaces,omitempty"`
	Neighbors  []Neighbor  `json:"neighbors,omitempty"`
	FDB        []FDBEntry  `json:"fdb,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// Interface is a port or logical interface.
type Interface struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Alias     string `json:"alias,omitempty"`
	Type      int    `json:"type,omitempty"`
	SpeedMbps uint64 `json:"speedMbps,omitempty"`
	MAC       string `json:"mac,omitempty"`
	Up        bool   `json:"up"`
	AdminUp   bool   `json:"adminUp"`
	InBytes   uint64 `json:"inBytes,omitempty"`
	OutBytes  uint64 `json:"outBytes,omitempty"`
}

// Neighbor is an LLDP neighbour seen on a local port.
type Neighbor struct {
	LocalPort string `json:"localPort"`
	ChassisID string `json:"chassisId,omitempty"`
	PortID    string `json:"portId,omitempty"`
	PortDescr string `json:"portDescr,omitempty"`
	SysName   string `json:"sysName,omitempty"`
}

// FDBEntry is a MAC address learned on a port.
type FDBEntry struct {
	MAC  string `json:"mac"`
	Port string `json:"port"`
	VLAN int    `json:"vlan,omitempty"`
}

// OIDs (MIB-II, IF-MIB, LLDP-MIB, BRIDGE-MIB, Q-BRIDGE-MIB).
const (
	oidSys        = ".1.3.6.1.2.1.1"
	oidIfDescr    = ".1.3.6.1.2.1.2.2.1.2"
	oidIfType     = ".1.3.6.1.2.1.2.2.1.3"
	oidIfSpeed    = ".1.3.6.1.2.1.2.2.1.5"
	oidIfMAC      = ".1.3.6.1.2.1.2.2.1.6"
	oidIfAdmin    = ".1.3.6.1.2.1.2.2.1.7"
	oidIfOper     = ".1.3.6.1.2.1.2.2.1.8"
	oidIfName     = ".1.3.6.1.2.1.31.1.1.1.1"
	oidIfHCIn     = ".1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOut    = ".1.3.6.1.2.1.31.1.1.1.10"
	oidIfHigh     = ".1.3.6.1.2.1.31.1.1.1.15"
	oidIfAlias    = ".1.3.6.1.2.1.31.1.1.1.18"
	oidLldpLocID  = ".1.0.8802.1.1.2.1.3.7.1.3"
	oidLldpLocDsc = ".1.0.8802.1.1.2.1.3.7.1.4"
	oidLldpRem    = ".1.0.8802.1.1.2.1.4.1.1"
	oidBasePortIf = ".1.3.6.1.2.1.17.1.4.1.2"
	oidFdbPort    = ".1.3.6.1.2.1.17.4.3.1.2"
	oidQFdbPort   = ".1.3.6.1.2.1.17.7.1.2.2.1.2"
)

// Walker is the part of an SNMP session the poll needs (gosnmp.GoSNMP).
type Walker interface {
	BulkWalkAll(root string) ([]gosnmp.SnmpPDU, error)
}

// Poll reads one device.
func Poll(w Walker, host string) Device {
	d := Device{Host: host}
	sys, err := w.BulkWalkAll(oidSys)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	for _, p := range sys {
		switch strings.TrimPrefix(p.Name, oidSys) {
		case ".1.0":
			d.Descr = str(p)
		case ".2.0":
			d.ObjectID = str(p)
		case ".3.0":
			d.UptimeSec = int64(num(p) / 100)
		case ".4.0":
			d.Contact = str(p)
		case ".5.0":
			d.SysName = str(p)
		case ".6.0":
			d.Location = str(p)
		case ".7.0":
			d.Services = int(num(p))
		}
	}
	d.Interfaces = interfaces(w)
	names := map[int]string{}
	for _, i := range d.Interfaces {
		names[i.Index] = i.Name
	}
	d.Neighbors = neighbors(w)
	d.FDB = fdb(w, names)
	return d
}

func walk(w Walker, root string) map[string]gosnmp.SnmpPDU {
	out := map[string]gosnmp.SnmpPDU{}
	pdus, err := w.BulkWalkAll(root)
	if err != nil {
		return out
	}
	for _, p := range pdus {
		out[strings.TrimPrefix(strings.TrimPrefix(p.Name, root), ".")] = p
	}
	return out
}

func interfaces(w Walker) []Interface {
	descr := walk(w, oidIfDescr)
	name, alias := walk(w, oidIfName), walk(w, oidIfAlias)
	typ, speed, high := walk(w, oidIfType), walk(w, oidIfSpeed), walk(w, oidIfHigh)
	mac, admin, oper := walk(w, oidIfMAC), walk(w, oidIfAdmin), walk(w, oidIfOper)
	in, out := walk(w, oidIfHCIn), walk(w, oidIfHCOut)
	var list []Interface
	for idx, p := range descr {
		n, err := strconv.Atoi(idx)
		if err != nil {
			continue
		}
		i := Interface{Index: n, Name: str(p)}
		if v, ok := name[idx]; ok && str(v) != "" {
			i.Name = str(v)
		}
		i.Alias = str(alias[idx])
		i.Type = int(num(typ[idx]))
		if v, ok := high[idx]; ok && num(v) > 0 {
			i.SpeedMbps = num(v)
		} else {
			i.SpeedMbps = num(speed[idx]) / 1_000_000
		}
		i.MAC = macOf(mac[idx].Value)
		i.AdminUp = num(admin[idx]) == 1
		i.Up = num(oper[idx]) == 1
		i.InBytes, i.OutBytes = num(in[idx]), num(out[idx])
		list = append(list, i)
		if len(list) >= MaxInterfaces {
			break
		}
	}
	sortInterfaces(list)
	return list
}

func sortInterfaces(list []Interface) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Index < list[j-1].Index; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// neighbors reads lldpRemTable (index: timeMark.localPortNum.remIndex).
func neighbors(w Walker) []Neighbor {
	locID, locDescr := walk(w, oidLldpLocID), walk(w, oidLldpLocDsc)
	rem := walk(w, oidLldpRem)
	byKey := map[string]*Neighbor{}
	var keys []string
	for k, p := range rem {
		// k = column.timeMark.localPort.index
		parts := strings.SplitN(k, ".", 2)
		if len(parts) != 2 {
			continue
		}
		col, key := parts[0], parts[1]
		kp := strings.Split(key, ".")
		if len(kp) != 3 {
			continue
		}
		n := byKey[key]
		if n == nil {
			if len(byKey) >= MaxNeighbors {
				continue
			}
			local := str(locDescr[kp[1]])
			if local == "" {
				local = str(locID[kp[1]])
			}
			if local == "" {
				local = "port " + kp[1]
			}
			n = &Neighbor{LocalPort: local}
			byKey[key] = n
			keys = append(keys, key)
		}
		switch col {
		case "5":
			n.ChassisID = idString(p)
		case "7":
			n.PortID = idString(p)
		case "8":
			n.PortDescr = str(p)
		case "9":
			n.SysName = str(p)
		}
	}
	out := make([]Neighbor, 0, len(keys))
	for _, k := range sortedKeys(keys) {
		out = append(out, *byKey[k])
	}
	return out
}

// fdb reads the MAC table: Q-BRIDGE (with VLAN) if the device has it, else BRIDGE-MIB.
func fdb(w Walker, ifNames map[int]string) []FDBEntry {
	portIf := walk(w, oidBasePortIf)
	port := func(bridgePort uint64) string {
		if p, ok := portIf[strconv.FormatUint(bridgePort, 10)]; ok {
			if n := ifNames[int(num(p))]; n != "" {
				return n
			}
			return "if " + strconv.FormatUint(num(p), 10)
		}
		return "port " + strconv.FormatUint(bridgePort, 10)
	}
	var out []FDBEntry
	if q := walk(w, oidQFdbPort); len(q) > 0 {
		for k, p := range q {
			parts := strings.Split(k, ".")
			if len(parts) != 7 || num(p) == 0 {
				continue
			}
			vlan, _ := strconv.Atoi(parts[0])
			out = append(out, FDBEntry{MAC: macFromOID(parts[1:]), Port: port(num(p)), VLAN: vlan})
			if len(out) >= MaxFDB {
				break
			}
		}
	} else {
		for k, p := range walk(w, oidFdbPort) {
			parts := strings.Split(k, ".")
			if len(parts) != 6 || num(p) == 0 {
				continue
			}
			out = append(out, FDBEntry{MAC: macFromOID(parts), Port: port(num(p))})
			if len(out) >= MaxFDB {
				break
			}
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].MAC < out[j-1].MAC; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortedKeys(keys []string) []string {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func str(p gosnmp.SnmpPDU) string {
	switch v := p.Value.(type) {
	case []byte:
		return strings.TrimSpace(strings.ToValidUTF8(strings.TrimRight(string(v), "\x00"), ""))
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	}
	return fmt.Sprint(p.Value)
}

func num(p gosnmp.SnmpPDU) uint64 {
	if p.Value == nil {
		return 0
	}
	return gosnmp.ToBigInt(p.Value).Uint64()
}

func macOf(v any) string {
	b, ok := v.([]byte)
	if !ok || len(b) != 6 {
		return ""
	}
	return net.HardwareAddr(b).String()
}

// idString shows a chassis or port ID: a MAC if it is 6 bytes, else text.
func idString(p gosnmp.SnmpPDU) string {
	if m := macOf(p.Value); m != "" {
		return m
	}
	return str(p)
}

func macFromOID(parts []string) string {
	b := make([]byte, 0, 6)
	for _, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 255 {
			return ""
		}
		b = append(b, byte(n))
	}
	return net.HardwareAddr(b).String()
}

// Poller polls the configured targets every few minutes.
type Poller struct {
	Config Config
	// Dial opens a session (tests replace it).
	Dial func(ctx context.Context, t Target) (Walker, func(), error)

	mu   sync.Mutex
	last time.Time
}

// Read polls all targets when the interval is over and returns nil otherwise.
func (p *Poller) Read(ctx context.Context, now time.Time) []Device {
	every := time.Duration(p.Config.EveryMinutes) * time.Minute
	if every <= 0 {
		every = DefaultEvery
	}
	p.mu.Lock()
	if !p.last.IsZero() && now.Sub(p.last) < every {
		p.mu.Unlock()
		return nil
	}
	p.last = now
	p.mu.Unlock()
	dial := p.Dial
	if dial == nil {
		dial = Dial
	}
	out := make([]Device, 0, len(p.Config.Targets))
	for _, t := range p.Config.Targets {
		w, closeFn, err := dial(ctx, t)
		var d Device
		if err != nil {
			d = Device{Host: t.Host, Error: err.Error()}
		} else {
			d = Poll(w, t.Host)
			closeFn()
		}
		d.Name = t.Name
		out = append(out, d)
	}
	return out
}

// Dial connects to a target with the credentials from its files.
func Dial(ctx context.Context, t Target) (Walker, func(), error) {
	port := t.Port
	if port == 0 {
		port = 161
	}
	g := &gosnmp.GoSNMP{Target: t.Host, Port: port, Context: ctx, Timeout: 5 * time.Second, Retries: 1, MaxRepetitions: 25, MaxOids: gosnmp.MaxOids}
	switch t.Version {
	case "3":
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		sp := &gosnmp.UsmSecurityParameters{UserName: t.User}
		g.MsgFlags = gosnmp.NoAuthNoPriv
		if t.AuthKeyFile != "" {
			key, err := secret(t.AuthKeyFile)
			if err != nil {
				return nil, nil, err
			}
			sp.AuthenticationProtocol, sp.AuthenticationPassphrase = authProto(t.AuthProtocol), key
			g.MsgFlags = gosnmp.AuthNoPriv
			if t.PrivKeyFile != "" && !strings.EqualFold(t.PrivProtocol, "none") {
				pk, err := secret(t.PrivKeyFile)
				if err != nil {
					return nil, nil, err
				}
				sp.PrivacyProtocol, sp.PrivacyPassphrase = privProto(t.PrivProtocol), pk
				g.MsgFlags = gosnmp.AuthPriv
			}
		}
		g.SecurityParameters = sp
	default:
		community, err := secret(t.CommunityFile)
		if err != nil {
			return nil, nil, err
		}
		g.Version, g.Community = gosnmp.Version2c, community
	}
	if err := g.Connect(); err != nil {
		return nil, nil, err
	}
	return g, func() { _ = g.Conn.Close() }, nil
}

func secret(file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", errors.New(file + " is empty")
	}
	return s, nil
}

func authProto(s string) gosnmp.SnmpV3AuthProtocol {
	switch strings.ToUpper(s) {
	case "MD5":
		return gosnmp.MD5
	case "SHA256":
		return gosnmp.SHA256
	case "SHA512":
		return gosnmp.SHA512
	}
	return gosnmp.SHA
}

func privProto(s string) gosnmp.SnmpV3PrivProtocol {
	switch strings.ToUpper(s) {
	case "DES":
		return gosnmp.DES
	case "AES256":
		return gosnmp.AES256
	}
	return gosnmp.AES
}
