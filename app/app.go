// Package app is the Movie night controller: it owns the config, the WebRTC
// hub/web server, the tunnel and the ffmpeg pipelines, and is driven by the
// tray menu.
package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"movienight/assets"
	"movienight/audio"
	"movienight/capture"
	"movienight/stream"
	"movienight/tunnel"
	"movienight/web"
)

const (
	videoPort = 5004
	audioPort = 5006
)

type App struct {
	ffmpeg   string
	cfgPath  string
	Monitors []capture.Monitor

	mu       sync.Mutex
	cfg      Config
	sharing  bool           // a share session is open (link works)
	paused   bool           // session open, but the picture is frozen
	base     string         // public address of the web server ("" until known)
	encoders map[int]string // detected encoder per GPU adapter
	encoder  string
	status   string // last problem, shown in the tray

	ctx         context.Context
	hub         *stream.Hub
	lobby       *stream.Lobby
	reactions   *stream.Reactions
	handler     http.Handler
	srv         *http.Server
	localURL    string // where cloudflared forwards to
	netCancel   context.CancelFunc
	shareCancel context.CancelFunc
	ice         iceCache
	audio       *audio.Manager
	videoCancel context.CancelFunc
	audioCancel context.CancelFunc
	pipelines   sync.WaitGroup

	// OnRequest is called when someone asks to watch (the tray notifies).
	OnRequest func(stream.Request)

	changed chan struct{}
}

// State is a snapshot for the tray.
type State struct {
	Sharing  bool
	Paused   bool
	Viewers  []stream.Viewer
	Requests []stream.Request
	URL      string
	Encoder  string
	Status   string
	Config   Config
	Apps     []audio.AppStatus
}

func New(ffmpeg, cfgPath string) (*App, error) {
	mons, err := capture.ListMonitors(ffmpeg)
	if err != nil {
		return nil, err
	}
	cfg := loadConfig(cfgPath)
	if cfg.Monitor < 0 || cfg.Monitor >= len(mons) {
		cfg.Monitor = 0
	}
	return &App{
		ffmpeg: ffmpeg, cfgPath: cfgPath, Monitors: mons, cfg: cfg,
		encoders: map[int]string{}, changed: make(chan struct{}, 1),
	}, nil
}

// Changed signals the tray to refresh.
func (a *App) Changed() <-chan struct{} { return a.changed }

func (a *App) notify() {
	select {
	case a.changed <- struct{}{}:
	default:
	}
}

// Start begins watching audio apps and gets the network side ready. Nothing
// is shared, and no link exists, until StartSharing.
func (a *App) Start(ctx context.Context) error {
	a.ctx = ctx
	cfg := a.Config()

	if m, err := audio.NewManager(cfg.policy()); err != nil {
		log.Printf("audio: %v", err)
		a.setStatus("Sound unavailable: " + err.Error())
	} else {
		a.audio = m
		go m.Run(ctx)
	}

	hub, err := stream.NewHub()
	if err != nil {
		return err
	}
	lobby := stream.NewLobby()
	lobby.SetApproval(cfg.RequireApproval)
	lobby.OnChange = a.notify
	lobby.OnRequest = func(r stream.Request) {
		log.Printf("%s (%s) asks to watch", r.Name, r.IP)
		if a.OnRequest != nil {
			a.OnRequest(r)
		}
	}
	hub.OnViewers = a.notify
	hub.OnLeave = lobby.Gone
	a.reactions = stream.NewReactions()
	a.reactions.SetSounds(assets.SoundOdds())
	a.hub, a.lobby = hub, lobby
	a.handler = stream.Handler(hub, lobby, a.reactions, hostName(), web.Files(), assets.ViewerFiles())

	go a.forward(ctx, videoPort, "video")
	go a.forward(ctx, audioPort, "audio")

	if runtime.GOOS == "linux" && os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		a.setStatus("Wayland session: screen capture needs \"Ubuntu on Xorg\"")
	}
	return a.startNetwork()
}

// startNetwork opens the ICE port and the web server for the configured
// network mode.
func (a *App) startNetwork() error {
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.netCancel = cancel
	a.mu.Unlock()
	cfg := a.Config()

	if cfg.Network == NetInternet {
		// Reachable only through the tunnel: nothing listens on the LAN.
		if err := a.hub.Listen(net.IPv4zero, cfg.ICEPort, false); err != nil {
			return fmt.Errorf("%w (is Movie night already running?)", err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		a.serveOn(ln, "")
		return nil
	}

	// Tailscale only: wait for a tailnet address rather than exposing the LAN.
	go func() {
		ip := stream.TailscaleIP()
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for ip == nil {
			a.setStatus(tailscaleStatus(TailscaleState()))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			ip = stream.TailscaleIP()
		}
		if err := a.hub.Listen(ip, cfg.ICEPort, true); err != nil {
			a.setStatus(err.Error())
			return
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(cfg.Port)))
		if err != nil {
			a.setStatus(fmt.Sprintf("Can't open port %d: %v", cfg.Port, err))
			return
		}
		if ctx.Err() != nil {
			ln.Close()
			return
		}
		a.setStatus("")
		a.serveOn(ln, fmt.Sprintf("http://%s:%d", ip, cfg.Port))
	}()
	return nil
}

func (a *App) serveOn(ln net.Listener, base string) {
	srv := &http.Server{Handler: a.handler, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	a.mu.Lock()
	a.srv, a.localURL, a.base = srv, "http://"+ln.Addr().String(), base
	a.mu.Unlock()
	log.Printf("web server on %s", ln.Addr())
	a.notify()
}

func (a *App) stopNetwork() {
	a.StopSharing()
	a.mu.Lock()
	cancel, srv := a.netCancel, a.srv
	a.netCancel, a.srv, a.base, a.localURL = nil, nil, "", ""
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if srv != nil {
		srv.Close()
	}
	a.hub.Close()
}

// hostName is this computer's user name, shown in viewers' tab titles.
func hostName() string {
	if u, err := user.Current(); err == nil {
		n := u.Username[strings.LastIndexAny(u.Username, `\/`)+1:] // PC\Calvin → Calvin
		if n != "" {
			return n
		}
	}
	return "The host"
}

func (a *App) forward(ctx context.Context, port int, kind string) {
	if err := a.hub.Forward(ctx, port, kind); err != nil {
		log.Printf("[%s] %v", kind, err)
		a.setStatus(fmt.Sprintf("%s relay failed: %v", kind, err))
	}
}

func (a *App) setStatus(s string) {
	a.mu.Lock()
	changed := a.status != s
	a.status = s
	a.mu.Unlock()
	if changed {
		a.notify()
	}
}

func (a *App) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// ConfigPath is config.json, created with the current settings if missing,
// for hand edits (relay server).
func (a *App) ConfigPath() string {
	if _, err := os.Stat(a.cfgPath); err != nil {
		saveConfig(a.cfgPath, a.Config())
	}
	return a.cfgPath
}

// URL is the invite link, or "" while not sharing or still connecting.
func (a *App) URL() string {
	a.mu.Lock()
	sharing, base := a.sharing, a.base
	a.mu.Unlock()
	token := ""
	if a.lobby != nil {
		token = a.lobby.Token()
	}
	if !sharing || base == "" || token == "" {
		return ""
	}
	return base + "/s/" + token + "/"
}

func (a *App) State() State {
	a.mu.Lock()
	st := State{Sharing: a.sharing, Paused: a.paused, Encoder: a.encoder, Status: a.status, Config: a.cfg}
	a.mu.Unlock()
	st.URL = a.URL()
	if a.hub != nil {
		st.Viewers = a.hub.Viewers()
		st.Requests = a.lobby.Requests()
	}
	if a.audio != nil {
		st.Apps = a.audio.Apps()
	}
	return st
}

// update applies fn to the config, saves it and reports what changed.
func (a *App) update(fn func(c *Config)) (old, cur Config) {
	a.mu.Lock()
	old = a.cfg
	fn(&a.cfg)
	cur = a.cfg
	a.mu.Unlock()
	if err := saveConfig(a.cfgPath, cur); err != nil {
		log.Printf("save config: %v", err)
	}
	a.notify()
	return old, cur
}

// StartSharing opens a share session: a fresh link (through a new tunnel in
// internet mode) and the capture pipelines.
func (a *App) StartSharing() {
	a.mu.Lock()
	if a.sharing {
		a.mu.Unlock()
		return
	}
	a.sharing, a.paused = true, false
	// Pick up relay settings edited in config.json since launch.
	fresh := loadConfig(a.cfgPath)
	a.cfg.TURN, a.cfg.HideIP = fresh.TURN, fresh.HideIP
	ctx, cancel := context.WithCancel(a.ctx)
	a.shareCancel = cancel
	cfg, local := a.cfg, a.localURL
	a.mu.Unlock()

	a.lobby.Open()
	if cfg.Network == NetInternet && local == "" {
		a.setStatus("The web server isn't running; see the log")
	} else if cfg.Network == NetInternet {
		// Re-open the ICE sockets: IPv6 privacy addresses rotate daily, and
		// nobody is connected yet, so this costs nothing.
		if err := a.hub.Listen(net.IPv4zero, cfg.ICEPort, false); err != nil {
			log.Print(err)
			a.setStatus(err.Error())
		}
		go a.keepICEFresh(ctx)
		a.startTunnel(ctx, local)
	} else {
		a.hub.SetICE(stream.ICEConfig{}) // tailnet addresses only
	}
	a.startVideo()
	a.startAudio()
	a.notify()
}

func (a *App) startTunnel(ctx context.Context, local string) {
	bin, err := tunnel.Find()
	if err != nil {
		a.setStatus(err.Error())
		return
	}
	t := tunnel.New(bin, local)
	t.OnURL = func(u string) {
		a.mu.Lock()
		if ctx.Err() == nil {
			a.base = u
		}
		a.mu.Unlock()
		if u != "" {
			log.Printf("invite link: %s", a.URL())
		}
		a.notify()
	}
	go t.Run(ctx)
}

// keepICEFresh sets the STUN/TURN servers and renews minted relay logins.
func (a *App) keepICEFresh(ctx context.Context) {
	for {
		cfg := a.Config()
		servers, err := a.ice.get(cfg.TURN)
		if err != nil {
			log.Print(err)
			a.setStatus("Relay unavailable: " + err.Error())
		}
		a.hub.SetICE(stream.ICEConfig{Servers: servers, RelayOnly: cfg.RelayOnly()})
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// StopSharing ends the session: everyone is disconnected, waiting friends are
// told, the link dies and the tunnel closes.
func (a *App) StopSharing() {
	a.mu.Lock()
	was := a.sharing
	a.sharing, a.paused = false, false
	cancel := a.shareCancel
	a.shareCancel = nil
	if a.cfg.Network == NetInternet {
		a.base = ""
	}
	a.mu.Unlock()
	// Tell every page first and give the answers a moment to get out through
	// the tunnel, so viewers see the goodbye instead of a dropped connection.
	if a.lobby != nil && a.lobby.Close() > 0 && cancel != nil {
		time.Sleep(1500 * time.Millisecond)
	}
	if cancel != nil {
		cancel()
	}
	if a.hub != nil {
		a.hub.CloseAll()
	}
	if a.reactions != nil {
		a.reactions.Clear()
	}
	a.stopVideo()
	a.stopAudio()
	if was {
		log.Print("stopped sharing")
	}
	a.notify()
}

func (a *App) Sharing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sharing
}

// Pause freezes the picture and sound; viewers stay connected.
func (a *App) Pause() {
	a.mu.Lock()
	if !a.sharing || a.paused {
		a.mu.Unlock()
		return
	}
	a.paused = true
	a.mu.Unlock()
	a.stopVideo()
	a.stopAudio()
	a.notify()
}

func (a *App) Resume() {
	a.mu.Lock()
	if !a.sharing || !a.paused {
		a.mu.Unlock()
		return
	}
	a.paused = false
	a.mu.Unlock()
	a.startVideo()
	a.startAudio()
	a.notify()
}

// streaming reports whether the capture pipelines should run.
func (a *App) streaming() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sharing && !a.paused
}

func (a *App) Allow(ticket string) { a.lobby.Allow(ticket) }
func (a *App) Deny(ticket string)  { a.lobby.Deny(ticket) }

// Kick disconnects a viewer; they have to ask (and be allowed) again.
func (a *App) Kick(id string) {
	a.lobby.Kicked(id)
	a.hub.Remove(id)
}

// SetNetwork switches between the public link and Tailscale-only. Any open
// session is ended, because the link changes.
func (a *App) SetNetwork(mode string) {
	if mode != NetTailscale {
		mode = NetInternet
	}
	if a.Config().Network == mode {
		return
	}
	a.stopNetwork()
	a.update(func(c *Config) { c.Network = mode })
	a.setStatus("")
	if err := a.startNetwork(); err != nil {
		log.Print(err)
		a.setStatus(err.Error())
	}
}

func (a *App) SetRequireApproval(on bool) {
	a.update(func(c *Config) { c.RequireApproval = on })
	a.lobby.SetApproval(on)
}

// SkipApprovalThisRun lets anyone with the link in without saving that
// choice (headless mode has no menu to click Allow in).
func (a *App) SkipApprovalThisRun() { a.lobby.SetApproval(false) }

// SetHideIP sends media only through the relay, for viewers joining from now on.
func (a *App) SetHideIP(on bool) {
	_, cur := a.update(func(c *Config) { c.HideIP = on })
	ice := a.hub.ICE()
	ice.RelayOnly = cur.RelayOnly()
	a.hub.SetICE(ice)
}

func (a *App) SetMonitor(i int) {
	if i < 0 || i >= len(a.Monitors) {
		return
	}
	if old, _ := a.update(func(c *Config) { c.Monitor = i }); old.Monitor != i {
		a.restartVideo()
	}
}

func (a *App) SetQuality(id string) {
	if old, cur := a.update(func(c *Config) { c.Quality = presetByID(id).ID }); old.Quality != cur.Quality {
		a.restartVideo()
	}
}

// SetBitrate sets the advanced bitrate in kbps; 0 returns to the preset default.
func (a *App) SetBitrate(kbps int) {
	if old, cur := a.update(func(c *Config) { c.BitrateKbps = kbps }); old.EffectiveKbps() != cur.EffectiveKbps() {
		a.restartVideo()
	}
}

func (a *App) SetShareAudio(on bool) {
	a.update(func(c *Config) { c.ShareAudio = on })
	if a.streaming() {
		a.stopAudio()
		a.startAudio()
	}
}

func (a *App) SetAudioMode(mode string) {
	_, cur := a.update(func(c *Config) { c.AudioMode = mode })
	if a.audio != nil {
		a.audio.SetPolicy(cur.policy())
	}
}

// ToggleApp adds or removes an app from the whitelist.
func (a *App) ToggleApp(name string) {
	name = audio.Normalize(name)
	_, cur := a.update(func(c *Config) {
		for i, w := range c.Whitelist {
			if audio.Normalize(w) == name {
				c.Whitelist = append(c.Whitelist[:i:i], c.Whitelist[i+1:]...)
				return
			}
		}
		c.Whitelist = append(c.Whitelist, name)
	})
	if a.audio != nil {
		a.audio.SetPolicy(cur.policy())
	}
}

func (a *App) restartVideo() {
	if a.streaming() {
		a.stopVideo()
		a.startVideo()
	}
}

func (a *App) captureConfig() capture.Config {
	cfg := a.Config()
	return capture.Config{
		FFmpeg: a.ffmpeg, Monitor: a.Monitors[cfg.Monitor], FPS: FPS,
		Height: presetByID(cfg.Quality).Height, Bitrate: cfg.EffectiveKbps() * 1000,
		Cursor: true, VideoPort: videoPort, AudioPort: audioPort,
	}
}

func (a *App) startVideo() {
	cc := a.captureConfig()
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.videoCancel = cancel
	a.mu.Unlock()
	a.pipelines.Add(1)
	go func() {
		defer a.pipelines.Done()
		enc, err := a.encoderFor(cc)
		if err != nil {
			log.Printf("video: %v", err)
			a.setStatus("No working video encoder")
			return
		}
		cc.Encoder = enc
		log.Printf("video: %s → %dp @ %d fps, %d kbps, %s", cc.Monitor, cc.Height, cc.FPS, cc.Bitrate/1000, enc)
		capture.Supervise(ctx, "video", a.ffmpeg, capture.VideoArgs(cc, enc), nil)
	}()
}

func (a *App) encoderFor(cc capture.Config) (string, error) {
	a.mu.Lock()
	enc, ok := a.encoders[cc.Monitor.Adapter]
	pref := a.cfg.Encoder
	a.mu.Unlock()
	if !ok {
		var err error
		if enc, err = capture.DetectEncoder(cc, pref); err != nil {
			return "", err
		}
	}
	a.mu.Lock()
	a.encoders[cc.Monitor.Adapter] = enc
	a.encoder = enc
	a.mu.Unlock()
	a.notify()
	return enc, nil
}

func (a *App) stopVideo() {
	a.mu.Lock()
	cancel := a.videoCancel
	a.videoCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) startAudio() {
	if !a.Config().ShareAudio || a.audio == nil {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.audioCancel = cancel
	a.mu.Unlock()
	a.audio.SetActive(true)
	input, feed := a.audio.FFmpegInput()
	a.pipelines.Add(1)
	go func() {
		defer a.pipelines.Done()
		capture.Supervise(ctx, "audio", a.ffmpeg, capture.AudioArgs(a.captureConfig(), input), feed)
	}()
}

func (a *App) stopAudio() {
	a.mu.Lock()
	cancel := a.audioCancel
	a.audioCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if a.audio != nil {
		a.audio.SetActive(false)
	}
}

// Shutdown stops everything and restores audio routing.
func (a *App) Shutdown() {
	if a.hub != nil {
		a.stopNetwork()
	}
	a.pipelines.Wait()
	if a.audio != nil {
		a.audio.Close()
	}
}
