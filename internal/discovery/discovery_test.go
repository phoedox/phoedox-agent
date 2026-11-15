package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const arp = `IP address       HW type     Flags       HW address            Mask     Device
10.0.0.1         0x1         0x2         aa:bb:cc:00:00:01     *        eth0
10.0.0.5         0x1         0x2         aa:bb:cc:00:00:05     *        eth0
10.0.0.9         0x1         0x0         00:00:00:00:00:00     *        eth0
`

func TestParseARP(t *testing.T) {
	d := ParseARP(strings.NewReader(arp))
	if len(d) != 2 || d[0].IP != "10.0.0.1" || d[1].MAC != "aa:bb:cc:00:00:05" {
		t.Fatalf("%+v", d)
	}
}

func TestFinder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "arp")
	_ = os.WriteFile(file, []byte(arp), 0o600)
	f := &Finder{Config: Config{Enabled: true}, ARPFile: file, Browse: func(context.Context, time.Duration) []Device {
		return []Device{{IP: "10.0.0.5", Hostname: "nas", Services: []string{"_smb._tcp"}}, {IP: "10.0.0.7", Hostname: "printer", Services: []string{"_ipp._tcp"}}}
	}}
	now := time.Now()
	d := f.Read(context.Background(), now)
	if len(d) != 3 || d[1].Hostname != "nas" || d[1].MAC != "aa:bb:cc:00:00:05" || strings.Join(d[1].Via, ",") != "arp,mdns" || d[2].Via[0] != "mdns" {
		t.Fatalf("%+v", d)
	}
	if f.Read(context.Background(), now.Add(time.Minute)) != nil {
		t.Fatal("ran again too early")
	}
	if (&Finder{}).Read(context.Background(), now) != nil {
		t.Fatal("ran while off")
	}
}

func TestCollector(t *testing.T) {
	name := func(s string) dnsmessage.Name { n, _ := dnsmessage.NewName(s); return n }
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true})
	_ = b.StartAnswers()
	_ = b.PTRResource(dnsmessage.ResourceHeader{Name: name("_services._dns-sd._udp.local."), Class: dnsmessage.ClassINET}, dnsmessage.PTRResource{PTR: name("_ipp._tcp.local.")})
	_ = b.PTRResource(dnsmessage.ResourceHeader{Name: name("_ipp._tcp.local."), Class: dnsmessage.ClassINET}, dnsmessage.PTRResource{PTR: name("Office._ipp._tcp.local.")})
	_ = b.SRVResource(dnsmessage.ResourceHeader{Name: name("Office._ipp._tcp.local."), Class: dnsmessage.ClassINET}, dnsmessage.SRVResource{Target: name("printer.local."), Port: 631})
	_ = b.AResource(dnsmessage.ResourceHeader{Name: name("printer.local."), Class: dnsmessage.ClassINET}, dnsmessage.AResource{A: [4]byte{10, 0, 0, 7}})
	msg, _ := b.Finish()
	c := newCollector()
	types := c.add(msg, "10.0.0.7")
	d := c.devices()
	if len(types) != 1 || types[0] != "_ipp._tcp.local." || len(d) != 1 || d[0].Hostname != "printer" || d[0].IP != "10.0.0.7" || len(d[0].Services) != 1 || d[0].Services[0] != "_ipp._tcp" {
		t.Fatalf("%v %+v", types, d)
	}
}
