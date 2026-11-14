package jobs

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/phoedox/phoedox-agent/internal/policy"
)

func TestParseMAC(t *testing.T) {
	want := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x0f}
	for _, s := range []string{"aa:bb:cc:dd:ee:0f", "AA-BB-CC-DD-EE-0F", "aabbccddee0f", "AaBbCcDdEe0F"} {
		if got, err := ParseMAC(s); err != nil || got != want {
			t.Errorf("%q: %v %x", s, err, got)
		}
	}
	for _, s := range []string{"", "aa:bb:cc:dd:ee", "aa:bb-cc:dd:ee:ff", "aa:bb:cc:dd:ee:gg", "aabbccddeeffaa",
		"aa.bb.cc.dd.ee.ff", "a:bb:cc:dd:ee:fff", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", " aabbccddeeff",
		"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff\n"} {
		if _, err := ParseMAC(s); err == nil {
			t.Errorf("%q must be rejected", s)
		}
	}
}

func TestParseBroadcast(t *testing.T) {
	if a, err := ParseBroadcast(""); err != nil || a.String() != "255.255.255.255" {
		t.Fatalf("default: %v %v", a, err)
	}
	if a, err := ParseBroadcast("192.168.1.255"); err != nil || a.String() != "192.168.1.255" {
		t.Fatalf("directed: %v %v", a, err)
	}
	for _, s := range []string{"0.0.0.0", "127.0.0.1", "224.0.0.1", "ff02::1", "::ffff:192.168.1.255", "host.lan", "192.168.1.255:9", "192.168.1"} {
		if _, err := ParseBroadcast(s); err == nil {
			t.Errorf("%q must be rejected", s)
		}
	}
}

func TestMagicPacket(t *testing.T) {
	mac := [6]byte{1, 2, 3, 4, 5, 6}
	p := MagicPacket(mac)
	if len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) {
		t.Fatalf("header: % x", p[:6])
	}
	if !bytes.Equal(p[6:], bytes.Repeat(mac[:], 16)) {
		t.Fatal("body is not 16 × MAC")
	}
}

func TestWakeOnLAN(t *testing.T) {
	var addr string
	var pkt []byte
	r := &Runner{WOLSend: func(_ context.Context, a string, p []byte) error { addr, pkt = a, p; return nil }}
	res := r.Run(context.Background(), policy.Job{Action: policy.ActWakeOnLAN, Args: []string{"AA-BB-CC-DD-EE-FF", "192.168.1.255"}})
	if !res.OK || res.Output != "magic packet sent to aa:bb:cc:dd:ee:ff via 192.168.1.255:9" || addr != "192.168.1.255:9" || len(pkt) != 102 {
		t.Fatalf("%+v %s %d", res, addr, len(pkt))
	}
	res = r.Run(context.Background(), policy.Job{Action: policy.ActWakeOnLAN, Args: []string{"aabbccddeeff"}})
	if !res.OK || addr != "255.255.255.255:9" {
		t.Fatalf("default broadcast: %+v %s", res, addr)
	}
	for _, args := range [][]string{nil, {"nope"}, {"aabbccddeeff", "10.0.0.300"}, {"aabbccddeeff", "10.0.0.255", "x"}} {
		if res := r.Run(context.Background(), policy.Job{Action: policy.ActWakeOnLAN, Args: args}); res.OK {
			t.Errorf("%v must fail", args)
		}
	}
}

func TestSendRepeated(t *testing.T) {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	pkt := MagicPacket([6]byte{1, 2, 3, 4, 5, 6})
	start := time.Now()
	if err := sendRepeated(context.Background(), l.LocalAddr().String(), pkt, 3, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("packets were not spaced")
	}
	_ = l.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 200)
	for i := range 3 {
		n, _, err := l.ReadFrom(buf)
		if err != nil || !bytes.Equal(buf[:n], pkt) {
			t.Fatalf("packet %d: %v", i, err)
		}
	}
}

func TestSpeedTestAndTerminal(t *testing.T) {
	r := &Runner{}
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActSpeedTest}); res.OK {
		t.Fatal("speed test without instance")
	}
	r.SpeedTest = func(context.Context) (string, error) { return `{"down":1.0,"up":2.0,"ping":3.0}`, nil }
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActSpeedTest}); !res.OK || res.Output != `{"down":1.0,"up":2.0,"ping":3.0}` {
		t.Fatalf("%+v", res)
	}
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActSpeedTest, Args: []string{"x"}}); res.OK {
		t.Fatal("args must be rejected")
	}

	var got string
	r.Terminal = func(_ context.Context, id string) (string, error) {
		got = id
		return "session ended after 1s (exited)", nil
	}
	if res := r.Run(context.Background(), policy.Job{Action: policy.ActTerminal, Args: []string{"Ab3dE6gH9jK2"}}); !res.OK || got != "Ab3dE6gH9jK2" {
		t.Fatalf("%+v %q", res, got)
	}
	for _, args := range [][]string{nil, {"short"}, {"has-dash-1234"}, {"../../etc/passwd"}, {strings.Repeat("a", 65)}, {"Ab3dE6gH9jK2", "x"}} {
		if res := r.Run(context.Background(), policy.Job{Action: policy.ActTerminal, Args: args}); res.OK {
			t.Errorf("%v must fail", args)
		}
	}
}
