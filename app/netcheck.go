package app

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/pion/stun/v4"
)

// checkNetwork logs what kind of network the sharer is on, which explains
// most "a friend can't connect" reports:
//   - whether UDP gets out at all (if not, video can't flow without a relay);
//   - whether the IPv4 NAT keeps one public port for every destination
//     (friendly: direct connections usually work) or picks a new one per
//     destination (strict, "symmetric": friends behind strict NAT or on
//     mobile data can't connect directly);
//   - whether IPv6 works, which avoids NAT altogether.
//
// It asks two public STUN servers what address they see, from one socket.
func checkNetwork(icePort int) {
	log.Printf("network check: %s", checkIPv4())
	log.Printf("network check: %s", checkIPv6())
	log.Printf("network check: video uses UDP port %d on this PC; the browser's side and the path each friend used are logged when they join", icePort)
}

var (
	stunCloudflare = "stun.cloudflare.com:3478"
	stunGoogle     = "stun.l.google.com:19302"
)

func checkIPv4() string {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return "IPv4: can't open a UDP socket: " + err.Error()
	}
	defer conn.Close()
	a, errA := stunMapped(conn, stunCloudflare, "udp4")
	b, errB := stunMapped(conn, stunGoogle, "udp4")
	local := conn.LocalAddr().(*net.UDPAddr).Port
	switch {
	case errA != nil && errB != nil:
		return fmt.Sprintf("IPv4: no STUN server answered (%v): outgoing UDP looks blocked (firewall, antivirus or VPN), so friends can only connect through a relay", errA)
	case errA != nil || errB != nil:
		if a == nil {
			a = b
		}
		return fmt.Sprintf("IPv4: public address %s (only one STUN server answered, so the NAT type is unknown)", a)
	case isLocalIP(a.IP):
		return fmt.Sprintf("IPv4: public address %s right on this PC (no NAT): friends can reach you directly", a.IP)
	case !a.IP.Equal(b.IP):
		return fmt.Sprintf("IPv4: strict NAT, a different public address per destination (%s and %s; carrier-grade NAT?): friends behind strict NAT or on mobile data can't connect directly, they need IPv6 or a relay", a.IP, b.IP)
	case a.Port != b.Port:
		return fmt.Sprintf("IPv4: strict (symmetric) NAT, public address %s but the port changes per destination (%d and %d): friends behind strict NAT or on mobile data can't connect directly, they need IPv6 or a relay", a.IP, a.Port, b.Port)
	default:
		kept := ""
		if a.Port == local {
			kept = ", port kept"
		}
		return fmt.Sprintf("IPv4: public address %s behind a friendly NAT (same public port %d for every destination%s): direct connections usually work", a.IP, a.Port, kept)
	}
}

func checkIPv6() string {
	var global []string
	addrs, _ := net.InterfaceAddrs()
	for _, ad := range addrs {
		if n, ok := ad.(*net.IPNet); ok && n.IP.To4() == nil && n.IP.IsGlobalUnicast() && !n.IP.IsPrivate() {
			global = append(global, n.IP.String())
		}
	}
	if len(global) == 0 {
		return "IPv6: no public IPv6 address on this PC, so everyone connects over IPv4"
	}
	conn, err := net.ListenUDP("udp6", nil)
	if err != nil {
		return "IPv6: can't open a UDP socket: " + err.Error()
	}
	defer conn.Close()
	m, err := stunMapped(conn, stunGoogle, "udp6")
	if err != nil {
		m, err = stunMapped(conn, stunCloudflare, "udp6")
	}
	if err != nil {
		return fmt.Sprintf("IPv6: address %s, but no STUN server answered over IPv6 (%v): the router or provider may not route IPv6", strings.Join(global, ", "), err)
	}
	if isLocalIP(m.IP) {
		return fmt.Sprintf("IPv6: works, public address %s (no NAT): friends with IPv6 can usually connect directly, if the router lets the answers in", m.IP)
	}
	return fmt.Sprintf("IPv6: works, but seen from outside as %s (translated)", m.IP)
}

// stunMapped asks a STUN server which address and port it sees conn's
// packets come from.
func stunMapped(conn *net.UDPConn, server, network string) (*net.UDPAddr, error) {
	to, err := net.ResolveUDPAddr(network, server)
	if err != nil {
		return nil, err
	}
	req, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	for try := 0; try < 3; try++ {
		if _, err := conn.WriteToUDP(req.Raw, to); err != nil {
			return nil, err
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break // ask again
				}
				return nil, err
			}
			if !from.IP.Equal(to.IP) {
				continue
			}
			res := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if res.Decode() != nil || res.TransactionID != req.TransactionID {
				continue
			}
			var xor stun.XORMappedAddress
			if err := xor.GetFrom(res); err != nil {
				return nil, err
			}
			return &net.UDPAddr{IP: xor.IP, Port: xor.Port}, nil
		}
	}
	return nil, fmt.Errorf("%s didn't answer", server)
}

func isLocalIP(ip net.IP) bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
