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
	"sync"
	"time"

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

type Hub struct {
	api          *webrtc.API
	video, audio *webrtc.TrackLocalStaticRTP

	mu    sync.Mutex
	peers map[string]*webrtc.PeerConnection

	// OnViewers is called whenever the number of connected viewers changes.
	OnViewers func(n int)
}

// NewHub listens for ICE on a single UDP port (one firewall rule) bound to
// listenIP. With an unspecified listenIP, only Tailscale addresses are offered
// unless allowLAN is set.
func NewHub(listenIP net.IP, icePort int, allowLAN bool) (*Hub, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	// NACK retransmission, RTCP reports, PLI handling.
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
		return nil, err
	}

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: listenIP, Port: icePort})
	if err != nil {
		return nil, fmt.Errorf("ICE port: %w", err)
	}
	se := webrtc.SettingEngine{}
	se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udp))
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	if !allowLAN {
		se.SetIPFilter(IsTailnet)
	}

	h := &Hub{
		api:   webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se)),
		peers: map[string]*webrtc.PeerConnection{},
	}
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
func (h *Hub) Answer(offerSDP string) (id, answerSDP string, err error) {
	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
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
	for _, t := range []*webrtc.TrackLocalStaticRTP{h.video, h.audio} {
		sender, err := pc.AddTrack(t)
		if err != nil {
			return fail(err)
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
	h.mu.Lock()
	h.peers[id] = pc
	h.mu.Unlock()
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			h.Remove(id)
		}
		log.Printf("viewer %s: %s (%d watching)", id, s, h.Viewers())
		if h.OnViewers != nil {
			h.OnViewers(h.Viewers())
		}
	})
	return id, pc.LocalDescription().SDP, nil
}

func (h *Hub) Remove(id string) bool {
	h.mu.Lock()
	pc, ok := h.peers[id]
	delete(h.peers, id)
	h.mu.Unlock()
	if ok {
		pc.Close()
	}
	return ok
}

// Viewers counts viewers that are currently connected.
func (h *Hub) Viewers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, pc := range h.peers {
		if pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			n++
		}
	}
	return n
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, pc := range h.peers {
		pc.Close()
		delete(h.peers, id)
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
