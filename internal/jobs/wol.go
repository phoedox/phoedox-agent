package jobs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// wolPort is the UDP port of Wake-on-LAN magic packets ("discard").
const wolPort = 9

// wolRepeat and wolGap: the packet is sent several times because UDP
// broadcasts can get lost and nobody acknowledges them.
const (
	wolRepeat = 3
	wolGap    = 100 * time.Millisecond
)

// ParseMAC accepts aa:bb:cc:dd:ee:ff, aa-bb-cc-dd-ee-ff or aabbccddeeff
// (any letter case). Separators must be consistent; the all-zero and the
// broadcast address are rejected.
func ParseMAC(s string) ([6]byte, error) {
	var mac [6]byte
	hexStr := s
	switch len(s) {
	case 17:
		sep := s[2]
		if sep != ':' && sep != '-' {
			return mac, fmt.Errorf("invalid MAC address %q", s)
		}
		parts := strings.Split(s, string(sep))
		if len(parts) != 6 {
			return mac, fmt.Errorf("invalid MAC address %q", s)
		}
		for _, p := range parts {
			if len(p) != 2 {
				return mac, fmt.Errorf("invalid MAC address %q", s)
			}
		}
		hexStr = strings.Join(parts, "")
	case 12:
	default:
		return mac, fmt.Errorf("invalid MAC address %q", s)
	}
	if _, err := hex.Decode(mac[:], []byte(hexStr)); err != nil {
		return mac, fmt.Errorf("invalid MAC address %q", s)
	}
	if mac == [6]byte{} || mac == [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff} {
		return mac, fmt.Errorf("invalid MAC address %q", s)
	}
	return mac, nil
}

// ParseBroadcast returns the IPv4 target of the magic packet; empty means
// the limited broadcast 255.255.255.255.
func ParseBroadcast(s string) (netip.Addr, error) {
	if s == "" {
		return netip.AddrFrom4([4]byte{255, 255, 255, 255}), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("invalid broadcast address %q (IPv4 expected)", s)
	}
	return a, nil
}

// MagicPacket builds the 102-byte Wake-on-LAN payload: six 0xFF bytes
// followed by the MAC address sixteen times.
func MagicPacket(mac [6]byte) []byte {
	b := make([]byte, 0, 6+16*6)
	for range 6 {
		b = append(b, 0xff)
	}
	for range 16 {
		b = append(b, mac[:]...)
	}
	return b
}

// formatMAC prints a MAC as aa:bb:cc:dd:ee:ff.
func formatMAC(mac [6]byte) string {
	return net.HardwareAddr(mac[:]).String()
}

func (r *Runner) wakeOnLAN(ctx context.Context, macArg, bcastArg string) (string, error) {
	mac, err := ParseMAC(macArg)
	if err != nil {
		return "", err
	}
	bcast, err := ParseBroadcast(bcastArg)
	if err != nil {
		return "", err
	}
	addr := netip.AddrPortFrom(bcast, wolPort).String()
	send := r.WOLSend
	if send == nil {
		send = func(ctx context.Context, addr string, pkt []byte) error {
			return sendRepeated(ctx, addr, pkt, wolRepeat, wolGap)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := send(ctx, addr, MagicPacket(mac)); err != nil {
		return "", fmt.Errorf("send magic packet: %w", err)
	}
	return "magic packet sent to " + formatMAC(mac) + " via " + addr, nil
}

// sendRepeated sends pkt n times over outbound UDP, gap apart. Go enables
// SO_BROADCAST on UDP sockets, so broadcast addresses work.
func sendRepeated(ctx context.Context, addr string, pkt []byte, n int, gap time.Duration) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp4", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	for i := range n {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(gap):
			}
		}
		if _, err := conn.Write(pkt); err != nil {
			return err
		}
	}
	if n <= 0 {
		return errors.New("nothing to send")
	}
	return nil
}
