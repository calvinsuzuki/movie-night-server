package stream

import (
	"net"
	"testing"
)

func TestSummarizeCandidates(t *testing.T) {
	sdp := "v=0\r\n" +
		"a=candidate:1 1 udp 2122260223 4f1c0b2e-1.local 51000 typ host generation 0\r\n" +
		"a=candidate:2 1 udp 1686052607 177.220.178.18 40001 typ srflx raddr 0.0.0.0 rport 0\r\n" +
		"a=candidate:3 1 udp 1686052863 2001:1284:f526:5b5b::1 51001 typ srflx raddr :: rport 0\r\n" +
		"a=candidate:4 1 tcp 1518280447 4f1c0b2e-1.local 9 typ host tcptype active\r\n" +
		"a=candidate:5 1 udp 2122260223 9a8b7c6d-2.local 51002 typ host\r\n"
	want := "host mdns, srflx IPv4 177.220.178.18, srflx IPv6 2001:1284:f526:5b5b::1"
	if got := summarizeCandidates(sdp); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := summarizeCandidates("v=0\r\n"); got == "" {
		t.Fatal("empty summary for an offer without candidates")
	}
}

func TestUsableIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"192.168.0.10":              true,
		"177.34.168.23":             true,
		"2804:14d:80a0:8030::1":     true,
		"fe80::1":                   false,
		"fd7a:115c:a1e0::8b30:f680": false,
		"fc00::1":                   false,
	} {
		if got := usableIP(net.ParseIP(addr)); got != want {
			t.Errorf("usableIP(%s) = %v, want %v", addr, got, want)
		}
	}
}
