// Package stream fans the RTP coming out of ffmpeg out to every connected
// browser over WebRTC. Nothing is decoded or re-encoded here.
package stream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Tailscale's CGNAT and ULA ranges.
var tailnets = []*net.IPNet{mustCIDR("100.64.0.0/10"), mustCIDR("fd7a:115c:a1e0::/48")}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func IsTailnet(ip net.IP) bool {
	for _, n := range tailnets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// TailscaleIP returns this machine's Tailscale IPv4 address, if any.
func TailscaleIP() net.IP {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && IsTailnet(ipn.IP) {
			return ipn.IP
		}
	}
	return nil
}

// Viewer is a connected friend, as shown in the tray.
type Viewer struct {
	ID, Name string
	Since    time.Time // when the video started flowing
}

type peer struct {
	pc        *webrtc.PeerConnection
	sender    *webrtc.RTPSender // to look up which network path was chosen
	name      string
	offered   string // the browser's candidates, summarized for the log
	connected time.Time
	dropTimer *time.Timer
}

// ICEConfig is what both ends use to find a path to each other: STUN servers
// to learn public addresses and, optionally, a TURN relay.
type ICEConfig struct {
	Servers   []webrtc.ICEServer
	RelayOnly bool // hide the sharer's IP: media only goes through the relay
}

type Hub struct {
	video, audio *webrtc.TrackLocalStaticRTP

	mu    sync.Mutex
	api   *webrtc.API
	mux   io.Closer // the ICE socket(s)
	ice   ICEConfig
	peers map[string]*peer
	ver   uint64        // bumps whenever someone starts or stops watching
	verCh chan struct{} // closed (and replaced) on every bump

	// OnViewers is called whenever the set of connected viewers changes.
	OnViewers func()
	// OnLeave is called once when a viewer's connection is gone for good.
	OnLeave func(id string)
}

// NewHub creates the shared video/audio tracks. Call Listen before Answer.
func NewHub() (*Hub, error) {
	h := &Hub{peers: map[string]*peer{}, verCh: make(chan struct{})}
	var err error
	// Constrained baseline: the profile every browser (incl. iOS) hardware-decodes.
	h.video, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "video", "screen")
	if err != nil {
		return nil, err
	}
	h.audio, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1",
	}, "audio", "screen")
	if err != nil {
		return nil, err
	}
	return h, nil
}

// Virtual adapters whose addresses no friend can reach.
var skipInterfaces = []string{"vethernet", "wsl", "docker", "virtualbox", "vmware", "hyper-v", "loopback", "br-", "veth"}

// Listen (re)opens the UDP port ICE uses (one firewall rule), dropping every
// viewer. With tailnetOnly it listens on listenIP and only Tailscale
// addresses are offered. Otherwise it listens on every IPv4 and IPv6
// address: LAN and public (STUN) IPv4, and IPv6, which usually needs no NAT
// traversal at all and so reaches friends whose IPv4 is behind strict NAT.
func (h *Hub) Listen(listenIP net.IP, icePort int, tailnetOnly bool) error {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}
	// NACK retransmission, RTCP reports, PLI handling.
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
		return err
	}

	h.Close()
	se := webrtc.SettingEngine{}
	var mux ice.UDPMux
	if tailnetOnly {
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: listenIP, Port: icePort})
		if err != nil {
			return fmt.Errorf("ICE port %d: %w", icePort, err)
		}
		mux = webrtc.NewICEUDPMux(nil, udp)
		se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		se.SetIPFilter(IsTailnet)
	} else {
		var err error
		mux, err = ice.NewMultiUDPMuxFromPort(icePort,
			ice.UDPMuxFromPortWithInterfaceFilter(usableInterface),
			ice.UDPMuxFromPortWithIPFilter(usableIP),
			ice.UDPMuxFromPortWithReadBufferSize(1<<20))
		if err != nil {
			return fmt.Errorf("ICE port %d: %w", icePort, err)
		}
		se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
		se.SetInterfaceFilter(usableInterface)
		se.SetIPFilter(usableIP)
	}
	se.SetICEUDPMux(mux)

	h.mu.Lock()
	h.mux = mux
	h.api = webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se))
	h.mu.Unlock()
	return nil
}

func usableInterface(name string) bool {
	name = strings.ToLower(name)
	for _, s := range skipInterfaces {
		if strings.Contains(name, s) {
			return false
		}
	}
	return true
}

// usableIP drops addresses no friend can reach: IPv6 link-local and
// unique-local (fc00::/7, e.g. Tailscale's), which a remote browser can't route to.
func usableIP(ip net.IP) bool {
	if ip.To4() != nil {
		return true
	}
	return !ip.IsLinkLocalUnicast() && !(ip[0]&0xfe == 0xfc)
}

// SetICE sets the STUN/TURN servers for viewers joining from now on.
func (h *Hub) SetICE(c ICEConfig) {
	h.mu.Lock()
	h.ice = c
	h.mu.Unlock()
}

// ICE returns the current STUN/TURN servers (browsers get the same list).
func (h *Hub) ICE() ICEConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ice
}

// Forward reads ffmpeg's RTP from 127.0.0.1:port and writes it to every viewer.
// ffmpeg also sends RTCP to port+1; that is drained so Windows doesn't answer it
// with ICMP port-unreachable.
func (h *Hub) Forward(ctx context.Context, port int, kind string) error {
	track := h.video
	if kind == "audio" {
		track = h.audio
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return err
	}
	rtcp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + 1})
	if err != nil {
		conn.Close()
		return err
	}
	conn.SetReadBuffer(4 << 20) // keyframes arrive as bursts of ~100 packets
	go func() {
		<-ctx.Done()
		conn.Close()
		rtcp.Close()
	}()
	go io.Copy(io.Discard, readerOf(rtcp))

	clock := uint32(90000)
	if kind == "audio" {
		clock = 48000
	}
	rw := &rewriter{clock: clock}
	buf := make([]byte, 1600)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		pkt := &rtp.Packet{}
		if pkt.Unmarshal(buf[:n]) != nil {
			continue
		}
		rw.rewrite(pkt)
		if err := track.WriteRTP(pkt); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			log.Printf("[%s] forward: %v", kind, err)
		}
	}
}

// rewriter keeps sequence numbers and timestamps continuous across ffmpeg
// restarts (quality/monitor change, pause), which otherwise start a new random
// RTP stream that browsers would treat as a huge jump and stall on.
type rewriter struct {
	clock    uint32
	init     bool
	ssrc     uint32
	seqOff   uint16
	tsOff    uint32
	lastSeq  uint16
	lastTS   uint32
	lastTime time.Time
}

func (r *rewriter) rewrite(p *rtp.Packet) {
	if !r.init || p.SSRC != r.ssrc {
		if r.init {
			gap := uint32(time.Since(r.lastTime).Seconds()*float64(r.clock)) + 1
			r.seqOff = r.lastSeq + 1 - p.SequenceNumber
			r.tsOff = r.lastTS + gap - p.Timestamp
		}
		r.init, r.ssrc = true, p.SSRC
	}
	p.SequenceNumber += r.seqOff
	p.Timestamp += r.tsOff
	r.lastSeq, r.lastTS, r.lastTime = p.SequenceNumber, p.Timestamp, time.Now()
}

type packetReader struct{ c net.PacketConn }

func (r packetReader) Read(p []byte) (int, error) {
	n, _, err := r.c.ReadFrom(p)
	return n, err
}

func readerOf(c net.PacketConn) io.Reader { return packetReader{c} }

// Answer creates a viewer connection for a browser's SDP offer and returns the
// answer (with all candidates; no trickle ICE) plus the viewer's id.
func (h *Hub) Answer(offerSDP, name string) (id, answerSDP string, err error) {
	h.mu.Lock()
	api, ice := h.api, h.ice
	h.mu.Unlock()
	if api == nil {
		return "", "", errors.New("not listening")
	}
	cfg := webrtc.Configuration{ICEServers: ice.Servers}
	if ice.RelayOnly {
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	pc, err := api.NewPeerConnection(cfg)
	if err != nil {
		return "", "", err
	}
	fail := func(err error) (string, string, error) {
		pc.Close()
		return "", "", err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return fail(err)
	}
	var first *webrtc.RTPSender
	for _, t := range []*webrtc.TrackLocalStaticRTP{h.video, h.audio} {
		sender, err := pc.AddTrack(t)
		if err != nil {
			return fail(err)
		}
		if first == nil {
			first = sender
		}
		// RTCP must be read for the interceptors (NACK, reports) to run.
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := sender.Read(buf); err != nil {
					return
				}
			}
		}()
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(err)
	}
	<-gathered

	id = newID()
	p := &peer{pc: pc, sender: first, name: name, offered: summarizeCandidates(offerSDP)}
	h.mu.Lock()
	h.peers[id] = p
	h.mu.Unlock()
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		h.mu.Lock()
		if p.dropTimer != nil {
			p.dropTimer.Stop()
			p.dropTimer = nil
		}
		switch s {
		case webrtc.PeerConnectionStateConnected:
			if p.connected.IsZero() {
				p.connected = time.Now()
				h.bump()
				go log.Printf("viewer %s (%s) path: %s", id, name, selectedPath(p.sender))
			}
		case webrtc.PeerConnectionStateDisconnected:
			// Give a flaky network 10 s to come back before cutting them off.
			p.dropTimer = time.AfterFunc(10*time.Second, func() { h.Remove(id) })
		}
		h.mu.Unlock()
		log.Printf("viewer %s (%s): %s", id, name, s)
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			h.Remove(id)
		} else if h.OnViewers != nil {
			h.OnViewers()
		}
	})
	return id, pc.LocalDescription().SDP, nil
}

// Remove closes a viewer's connection completely and reports whether the
// viewer existed.
func (h *Hub) Remove(id string) bool {
	h.mu.Lock()
	p, ok := h.peers[id]
	delete(h.peers, id)
	if ok && p.dropTimer != nil {
		p.dropTimer.Stop()
	}
	if ok && !p.connected.IsZero() {
		h.bump()
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	p.pc.Close()
	if p.connected.IsZero() {
		// Explains "couldn't reach the host directly" afterwards.
		log.Printf("viewer %s (%s) never connected; the browser offered: %s", id, p.name, p.offered)
	}
	log.Printf("viewer %s (%s) left (%d watching)", id, p.name, h.Count())
	if h.OnLeave != nil {
		h.OnLeave(id)
	}
	if h.OnViewers != nil {
		h.OnViewers()
	}
	return true
}

// Viewers lists the connected viewers, longest-watching first.
func (h *Hub) Viewers() []Viewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	var vs []Viewer
	for id, p := range h.peers {
		if p.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			vs = append(vs, Viewer{ID: id, Name: p.name, Since: p.connected})
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Since.Before(vs[j].Since) })
	return vs
}

// bump records a change in who's watching; h.mu must be held.
func (h *Hub) bump() {
	h.ver++
	close(h.verCh)
	h.verCh = make(chan struct{})
}

// WaitWatchers returns the watcher list version once it differs from since,
// or after timeout (long polling for the viewer page's watcher list).
func (h *Hub) WaitWatchers(since uint64, timeout time.Duration) uint64 {
	h.mu.Lock()
	ver, ch := h.ver, h.verCh
	h.mu.Unlock()
	if ver != since {
		return ver
	}
	select {
	case <-ch:
	case <-time.After(timeout):
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ver
}

// Count is the number of connected viewers.
func (h *Hub) Count() int { return len(h.Viewers()) }

// CloseAll disconnects every viewer.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.peers))
	for id := range h.peers {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.Remove(id)
	}
}

// Close disconnects everyone and frees the ICE port.
func (h *Hub) Close() {
	h.CloseAll()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mux != nil {
		h.mux.Close()
		h.mux, h.api = nil, nil
	}
}

// summarizeCandidates lists the network paths a browser offered, e.g.
// "host mdns, srflx IPv4 177.1.2.3, srflx IPv6 2001:db8::1".
func summarizeCandidates(sdp string) string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		// foundation component protocol priority address port "typ" type ...
		f := strings.Fields(strings.TrimPrefix(line, "a=candidate:"))
		if len(f) < 8 || !strings.EqualFold(f[2], "udp") {
			continue
		}
		addr, typ := f[4], f[7]
		desc := typ + " "
		switch ip := net.ParseIP(addr); {
		case strings.HasSuffix(addr, ".local"):
			desc += "mdns" // hidden local address
		case ip == nil:
			desc += addr
		case ip.To4() != nil:
			desc += "IPv4 " + addr
		default:
			desc += "IPv6 " + addr
		}
		if !seen[desc] {
			seen[desc] = true
			out = append(out, desc)
		}
	}
	if len(out) == 0 {
		return "no UDP candidates (VPN or firewall blocking WebRTC?)"
	}
	return strings.Join(out, ", ")
}

// selectedPath describes the candidate pair a connected viewer uses.
func selectedPath(s *webrtc.RTPSender) string {
	if s == nil || s.Transport() == nil {
		return "?"
	}
	pair, err := s.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return "?"
	}
	family := "IPv4"
	if ip := net.ParseIP(pair.Remote.Address); ip != nil && ip.To4() == nil {
		family = "IPv6"
	}
	return fmt.Sprintf("%s, us %s %s:%d ↔ them %s %s:%d", family,
		pair.Local.Typ, pair.Local.Address, pair.Local.Port, pair.Remote.Typ, pair.Remote.Address, pair.Remote.Port)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newToken is an unguessable 128-bit id for links and tickets.
func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
