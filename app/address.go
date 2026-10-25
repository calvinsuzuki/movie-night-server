package app

import (
	"net"
	"sort"
	"strings"

	"movienight/stream"
)

// Address is one of this computer's own addresses that a share can be tied
// to instead of the internet link: the home network, or Tailscale.
type Address struct {
	IP   net.IP
	Name string // "Tailscale", or the network adapter, e.g. "Wi-Fi"
}

func (a Address) String() string { return a.Name + " · " + a.IP.String() }

// Addresses lists the IPv4 addresses friends could reach directly: private
// LAN ranges and Tailscale, Tailscale first. Virtual adapters (WSL, Docker,
// VMs) are left out.
func Addresses() []Address {
	ifaces, _ := net.Interfaces()
	var out []Address
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip := ipn.IP.To4()
			switch {
			case stream.IsTailnet(ip):
				out = append(out, Address{IP: ip, Name: "Tailscale"})
			case ip.IsPrivate() && !virtual(ifc.Name):
				out = append(out, Address{IP: ip, Name: ifc.Name})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name == "Tailscale" && out[j].Name != "Tailscale" })
	return out
}

func virtual(name string) bool {
	name = strings.ToLower(name)
	for _, s := range []string{"vethernet", "wsl", "docker", "virtualbox", "vmware", "hyper-v", "br-", "veth", "tailscale"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}
