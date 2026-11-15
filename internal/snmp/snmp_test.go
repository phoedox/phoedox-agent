package snmp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

type fake map[string][]gosnmp.SnmpPDU

func (f fake) BulkWalkAll(root string) ([]gosnmp.SnmpPDU, error) {
	if v, ok := f[root]; ok {
		return v, nil
	}
	return nil, nil
}

func pdu(name string, v any) gosnmp.SnmpPDU { return gosnmp.SnmpPDU{Name: name, Value: v} }

func TestPoll(t *testing.T) {
	f := fake{
		oidSys:        {pdu(oidSys+".1.0", []byte("Cisco IOS Software, C2960")), pdu(oidSys+".3.0", uint32(360000)), pdu(oidSys+".5.0", []byte("core-sw")), pdu(oidSys+".6.0", []byte("Rack 1")), pdu(oidSys+".7.0", 2)},
		oidIfDescr:    {pdu(oidIfDescr+".1", []byte("GigabitEthernet0/1")), pdu(oidIfDescr+".2", []byte("GigabitEthernet0/2"))},
		oidIfName:     {pdu(oidIfName+".1", []byte("Gi0/1")), pdu(oidIfName+".2", []byte("Gi0/2"))},
		oidIfAlias:    {pdu(oidIfAlias+".1", []byte("uplink router"))},
		oidIfHigh:     {pdu(oidIfHigh+".1", uint32(1000))},
		oidIfMAC:      {pdu(oidIfMAC+".1", []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55})},
		oidIfOper:     {pdu(oidIfOper+".1", 1), pdu(oidIfOper+".2", 2)},
		oidIfAdmin:    {pdu(oidIfAdmin+".1", 1), pdu(oidIfAdmin+".2", 1)},
		oidLldpLocDsc: {pdu(oidLldpLocDsc+".1", []byte("Gi0/1"))},
		oidLldpRem: {
			pdu(oidLldpRem+".5.0.1.1", []byte{0xaa, 0xbb, 0xcc, 0, 0, 1}),
			pdu(oidLldpRem+".7.0.1.1", []byte("eth3")),
			pdu(oidLldpRem+".9.0.1.1", []byte("fritzbox")),
		},
		oidBasePortIf: {pdu(oidBasePortIf+".2", 2)},
		oidFdbPort:    {pdu(oidFdbPort+".170.187.204.0.0.9", 2)},
	}
	d := Poll(f, "10.0.0.2")
	if d.SysName != "core-sw" || d.Location != "Rack 1" || d.UptimeSec != 3600 || d.Services != 2 || !strings.HasPrefix(d.Descr, "Cisco") {
		t.Fatalf("system: %+v", d)
	}
	if len(d.Interfaces) != 2 || d.Interfaces[0].Name != "Gi0/1" || !d.Interfaces[0].Up || d.Interfaces[1].Up || d.Interfaces[0].SpeedMbps != 1000 ||
		d.Interfaces[0].MAC != "00:11:22:33:44:55" || d.Interfaces[0].Alias != "uplink router" {
		t.Fatalf("interfaces: %+v", d.Interfaces)
	}
	if len(d.Neighbors) != 1 || d.Neighbors[0] != (Neighbor{LocalPort: "Gi0/1", ChassisID: "aa:bb:cc:00:00:01", PortID: "eth3", SysName: "fritzbox"}) {
		t.Fatalf("neighbors: %+v", d.Neighbors)
	}
	if len(d.FDB) != 1 || d.FDB[0] != (FDBEntry{MAC: "aa:bb:cc:00:00:09", Port: "Gi0/2"}) {
		t.Fatalf("fdb: %+v", d.FDB)
	}
}

type failing struct{}

func (failing) BulkWalkAll(string) ([]gosnmp.SnmpPDU, error) { return nil, errors.New("timeout") }

func TestPollerInterval(t *testing.T) {
	calls := 0
	p := &Poller{Config: Config{Targets: []Target{{Host: "10.0.0.2", Name: "Core"}}, EveryMinutes: 10},
		Dial: func(context.Context, Target) (Walker, func(), error) { calls++; return failing{}, func() {}, nil }}
	now := time.Now()
	d := p.Read(context.Background(), now)
	if len(d) != 1 || d[0].Error != "timeout" || d[0].Name != "Core" {
		t.Fatalf("%+v", d)
	}
	if p.Read(context.Background(), now.Add(5*time.Minute)) != nil || calls != 1 {
		t.Fatal("polled too early")
	}
	if p.Read(context.Background(), now.Add(11*time.Minute)) == nil || calls != 2 {
		t.Fatal("not polled again")
	}
}

func TestValidate(t *testing.T) {
	ok := Config{Targets: []Target{{Host: "sw", CommunityFile: "/etc/phoedox/community"}, {Host: "r", Version: "3", User: "phx", AuthKeyFile: "/etc/phoedox/auth"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Target{{Host: "sw"}, {Host: "sw", CommunityFile: "rel/path"}, {Host: "", CommunityFile: "/x"}, {Host: "r", Version: "3"}, {Host: "r", Version: "1", CommunityFile: "/x"}} {
		if (Config{Targets: []Target{bad}}).Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
