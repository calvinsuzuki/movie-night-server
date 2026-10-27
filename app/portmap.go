package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/jackpal/gateway"
	natpmp "github.com/jackpal/go-nat-pmp"
)

// A port mapping asks the home router to forward the ICE port to this PC, so
// friends behind strict NAT (mobile data, carrier-grade NAT) can reach us:
// their router picks a new public port for every destination, and ours drops
// packets from ports we never wrote to, so without it no direct path exists.
type portMapping struct {
	Local, Public net.IP // this PC's LAN address and the router's public one
	How           string // "UPnP" or "NAT-PMP"
	renew         func(ctx context.Context) error
	remove        func()
}

const mapLease = time.Hour

// mapPort asks the router (UPnP, then NAT-PMP) to forward UDP port to this PC
// on the same public port, which is what our ICE candidates advertise.
func mapPort(ctx context.Context, port int) (*portMapping, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m, errU := mapUPnP(ctx, port)
	if errU == nil {
		return m, nil
	}
	m, errP := mapNATPMP(port)
	if errP == nil {
		return m, nil
	}
	return nil, fmt.Errorf("UPnP: %v; NAT-PMP: %v", errU, errP)
}

// igd is what the three UPnP router service versions have in common.
type igd interface {
	AddPortMappingCtx(ctx context.Context, remoteHost string, externalPort uint16, protocol string, internalPort uint16, internalClient string, enabled bool, description string, leaseSeconds uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, externalPort uint16, protocol string) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
	LocalAddr() net.IP
}

func findIGDs(ctx context.Context) []igd {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []igd
	)
	add := func(c igd) { mu.Lock(); out = append(out, c); mu.Unlock() }
	wg.Add(3)
	go func() {
		defer wg.Done()
		cs, _, _ := internetgateway2.NewWANIPConnection2ClientsCtx(ctx)
		for _, c := range cs {
			add(c)
		}
	}()
	go func() {
		defer wg.Done()
		cs, _, _ := internetgateway2.NewWANIPConnection1ClientsCtx(ctx)
		for _, c := range cs {
			add(c)
		}
	}()
	go func() {
		defer wg.Done()
		cs, _, _ := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx)
		for _, c := range cs {
			add(c)
		}
	}()
	wg.Wait()
	return out
}

func mapUPnP(ctx context.Context, port int) (*portMapping, error) {
	clients := findIGDs(ctx)
	if len(clients) == 0 {
		return nil, errors.New("no router answered (UPnP off?)")
	}
	var last error
	for _, c := range clients {
		local := c.LocalAddr()
		if local == nil {
			continue
		}
		add := func(ctx context.Context) error {
			err := c.AddPortMappingCtx(ctx, "", uint16(port), "UDP", uint16(port), local.String(), true, "Movie night", uint32(mapLease.Seconds()))
			if err != nil {
				// Some routers only take permanent mappings; we delete it on stop.
				err = c.AddPortMappingCtx(ctx, "", uint16(port), "UDP", uint16(port), local.String(), true, "Movie night", 0)
			}
			return err
		}
		if err := add(ctx); err != nil {
			last = err
			continue
		}
		ext, err := c.GetExternalIPAddressCtx(ctx)
		public := net.ParseIP(ext)
		if err != nil || public == nil {
			c.DeletePortMappingCtx(ctx, "", uint16(port), "UDP")
			last = fmt.Errorf("router didn't say its public address (%v)", err)
			continue
		}
		return &portMapping{
			Local: local, Public: public, How: "UPnP", renew: add,
			remove: func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				c.DeletePortMappingCtx(ctx, "", uint16(port), "UDP")
			},
		}, nil
	}
	if last == nil {
		last = errors.New("no usable router")
	}
	return nil, last
}

func mapNATPMP(port int) (*portMapping, error) {
	gw, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, err
	}
	local, err := gateway.DiscoverInterface()
	if err != nil {
		return nil, err
	}
	c := natpmp.NewClientWithTimeout(gw, 2*time.Second)
	ext, err := c.GetExternalAddress()
	if err != nil {
		return nil, errors.New("router doesn't speak NAT-PMP")
	}
	add := func(context.Context) error {
		r, err := c.AddPortMapping("udp", port, port, int(mapLease.Seconds()))
		if err == nil && int(r.MappedExternalPort) != port {
			c.AddPortMapping("udp", port, 0, 0)
			return fmt.Errorf("router gave public port %d instead of %d", r.MappedExternalPort, port)
		}
		return err
	}
	if err := add(context.Background()); err != nil {
		return nil, err
	}
	a := ext.ExternalIPAddress
	return &portMapping{
		Local: local, Public: net.IPv4(a[0], a[1], a[2], a[3]), How: "NAT-PMP", renew: add,
		remove: func() { c.AddPortMapping("udp", port, 0, 0) }, // lifetime 0 deletes it
	}, nil
}

// Carrier-grade NAT (100.64.0.0/10): the router itself sits behind the
// provider's NAT, so opening a port on it doesn't reach the internet.
var cgnat = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// keepPortOpen maps the ICE port for the share session, advertises the
// router's public address to viewers, renews the lease, and removes the
// mapping when the session ends.
func (a *App) keepPortOpen(ctx context.Context, port int) {
	m, err := mapPort(ctx, port)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("router: couldn't open UDP port %d automatically (%v); friends behind strict NAT will watch through the link instead", port, err)
		}
		return
	}
	defer m.remove()
	if m.Public.IsPrivate() || cgnat.Contains(m.Public) || m.Public.IsUnspecified() {
		log.Printf("router: opened UDP port %d with %s, but the router's own address %s is behind your provider's NAT, so it doesn't help; friends behind strict NAT will watch through the link instead", port, m.How, m.Public)
		return
	}
	if err := a.hub.Advertise(m.Local, m.Public); err != nil {
		log.Printf("router: opened UDP port %d with %s, but can't use it: %v", port, m.How, err)
		return
	}
	log.Printf("router: opened UDP port %d with %s (%s:%d → %s:%d): friends behind strict NAT can connect directly", port, m.How, m.Public, port, m.Local, port)
	t := time.NewTicker(mapLease / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := m.renew(rctx); err != nil && ctx.Err() == nil {
				log.Printf("router: renewing the UDP port %d mapping failed: %v", port, err)
			}
			cancel()
		}
	}
}
