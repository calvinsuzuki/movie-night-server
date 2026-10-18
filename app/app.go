// Package app is the Movie night controller: it owns the config, the WebRTC
// hub/web server and the ffmpeg pipelines, and is driven by the tray menu.
package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"movienight/audio"
	"movienight/capture"
	"movienight/stream"
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
	sharing  bool
	url      string
	encoders map[int]string // detected encoder per GPU adapter
	encoder  string
	status   string // last problem, shown in the tray

	ctx         context.Context
	hub         *stream.Hub
	srv         *http.Server
	audio       *audio.Manager
	videoCancel context.CancelFunc
	audioCancel context.CancelFunc
	pipelines   sync.WaitGroup

	changed chan struct{}
}

// State is a snapshot for the tray.
type State struct {
	Sharing bool
	Viewers int
	URL     string
	Encoder string
	Status  string
	Config  Config
	Apps    []audio.AppStatus
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

// Start begins watching audio apps and serving viewers. Viewers are only
// served on the Tailscale address (unless AllowLAN), so if Tailscale isn't
// connected yet it waits for it instead of exposing the screen to the LAN.
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

	listenIP := stream.TailscaleIP()
	if listenIP == nil && !cfg.AllowLAN {
		log.Printf("waiting for Tailscale (%s)", TailscaleState())
		a.setStatus(tailscaleStatus(TailscaleState()))
		go func() {
			t := time.NewTicker(3 * time.Second)
			defer t.Stop()
			for listenIP == nil {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				if listenIP = stream.TailscaleIP(); listenIP == nil {
					a.setStatus(tailscaleStatus(TailscaleState())) // e.g. installed meanwhile
				}
			}
			a.setStatus("")
			if err := a.serve(ctx, listenIP); err != nil {
				log.Print(err)
				a.setStatus(err.Error())
			}
		}()
		return nil
	}
	return a.serve(ctx, listenIP)
}

// serve brings up the WebRTC hub and web server, then starts sharing.
func (a *App) serve(ctx context.Context, tsIP net.IP) error {
	cfg := a.Config()
	listenIP, host := tsIP, "localhost"
	if tsIP != nil {
		host = tsIP.String()
	}
	if cfg.AllowLAN {
		listenIP = net.IPv4zero
	}

	hub, err := stream.NewHub(listenIP, cfg.ICEPort, cfg.AllowLAN)
	if err != nil {
		return fmt.Errorf("%w (is Movie night already running?)", err)
	}
	hub.OnViewers = func(int) { a.notify() }

	ln, err := net.Listen("tcp", net.JoinHostPort(listenIP.String(), strconv.Itoa(cfg.Port)))
	if err != nil {
		hub.Close()
		return fmt.Errorf("can't open port %d: %w", cfg.Port, err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web.Files()))
	hub.Register(mux)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	a.mu.Lock()
	a.hub, a.srv = hub, srv
	a.url = fmt.Sprintf("http://%s:%d", host, cfg.Port)
	a.mu.Unlock()

	go a.forward(ctx, videoPort, "video")
	go a.forward(ctx, audioPort, "audio")

	if runtime.GOOS == "linux" && os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		a.setStatus("Wayland session: screen capture needs \"Ubuntu on Xorg\"")
	}
	log.Printf("viewers open: %s", a.URL())
	a.StartSharing()
	return nil
}

func (a *App) forward(ctx context.Context, port int, kind string) {
	if err := a.hub.Forward(ctx, port, kind); err != nil {
		log.Printf("[%s] %v", kind, err)
		a.setStatus(fmt.Sprintf("%s relay failed: %v", kind, err))
	}
}

func (a *App) setStatus(s string) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
	a.notify()
}

func (a *App) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// URL is the viewer link, or "" while waiting for Tailscale.
func (a *App) URL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.url
}

func (a *App) State() State {
	a.mu.Lock()
	st := State{Sharing: a.sharing, URL: a.url, Encoder: a.encoder, Status: a.status, Config: a.cfg}
	hub := a.hub
	a.mu.Unlock()
	if hub != nil {
		st.Viewers = hub.Viewers()
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

func (a *App) StartSharing() {
	a.mu.Lock()
	if a.sharing {
		a.mu.Unlock()
		return
	}
	a.sharing = true
	a.mu.Unlock()
	a.startVideo()
	a.startAudio()
	a.notify()
}

func (a *App) StopSharing() {
	a.mu.Lock()
	a.sharing = false
	a.mu.Unlock()
	a.stopVideo()
	a.stopAudio()
	a.notify()
}

func (a *App) Sharing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sharing
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
	if a.Sharing() {
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
	if a.Sharing() {
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
	a.StopSharing()
	a.pipelines.Wait()
	a.mu.Lock()
	srv, hub := a.srv, a.hub
	a.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
	if hub != nil {
		hub.Close()
	}
	if a.audio != nil {
		a.audio.Close()
	}
}
