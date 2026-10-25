//go:build linux

package audio

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"movienight/platform"
)

// On Linux each allowed app's stream (PulseAudio sink input) is recorded on
// its own with `parec --monitor-stream`, and the streams are mixed
// in-process, as on Windows. Apps stay on the speakers they were on, so the
// sharer hears them untouched, and nothing is left behind in the sound
// server if Movie night is killed: the parec helpers die with it. Works with
// PulseAudio and PipeWire's pulse server (Ubuntu 22.04+).

// AlertClient is the PulseAudio client name of Movie night's own alert
// sounds, which are never shared.
const AlertClient = "Movie night alerts"

// legacySink is the null sink older versions routed shared apps into. One
// killed mid-share stayed loaded, looped back to the speakers, and made
// every app routed through it stutter until the next reboot.
const legacySink = "movienight"

type Manager struct {
	mixer *Mixer

	mu       sync.Mutex
	policy   Policy
	active   bool
	apps     []AppStatus
	captures map[string]context.CancelFunc // by sink input index
	failed   map[string]time.Time
	kick     chan struct{}
}

func NewManager(p Policy) (*Manager, error) {
	for _, tool := range []string{"pactl", "parec"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%s not found (install pulseaudio-utils)", tool)
		}
	}
	unloadLegacy()
	return &Manager{
		mixer: NewMixer(), policy: p,
		captures: map[string]context.CancelFunc{}, failed: map[string]time.Time{},
		kick: make(chan struct{}, 1),
	}, nil
}

// unloadLegacy removes the null sink and loopback an older Movie night
// left behind, putting the apps on it back on the normal output.
func unloadLegacy() {
	out, _ := pactl("list", "short", "modules")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sink_name="+legacySink) || strings.Contains(line, "source="+legacySink+".monitor") {
			if f := strings.Fields(line); len(f) > 0 {
				log.Printf("audio: removing leftover module %s", line)
				pactl("unload-module", f[0])
			}
		}
	}
}

func (m *Manager) SetPolicy(p Policy) {
	m.mu.Lock()
	m.policy = p
	m.mu.Unlock()
	m.poke()
}

// SetActive starts or stops the per-app captures (only needed while sharing).
func (m *Manager) SetActive(on bool) {
	m.mu.Lock()
	m.active = on
	m.mu.Unlock()
	m.poke()
}

func (m *Manager) poke() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) Apps() []AppStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AppStatus(nil), m.apps...)
}

// FFmpegInput describes the raw PCM the mixer writes to ffmpeg's stdin.
func (m *Manager) FFmpegInput() ([]string, func(ctx context.Context, w io.Writer) error) {
	return []string{"-f", "s16le", "-ar", strconv.Itoa(MixRate), "-ac", strconv.Itoa(MixChannels), "-i", "pipe:0"},
		m.mixer.Pump
}

// Run reconciles the captures every two seconds, and right away when an
// app opens or closes a stream.
func (m *Manager) Run(ctx context.Context) {
	go m.watch(ctx)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		m.reconcile(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for id, cancel := range m.captures {
				cancel()
				delete(m.captures, id)
			}
			m.mu.Unlock()
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

// watch pokes the manager whenever a sink input comes or goes, so a video
// that starts playing is shared from its first second.
func (m *Manager) watch(ctx context.Context) {
	for ctx.Err() == nil {
		cmd := platform.Command(ctx, "pactl", "subscribe")
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			log.Printf("audio: pactl subscribe: %v", err)
			return
		}
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "on sink-input") && !strings.Contains(line, "'change'") {
				m.poke()
			}
		}
		cmd.Wait()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

type sinkInput struct {
	id, driver, binary, name string
}

func (m *Manager) reconcile(ctx context.Context) {
	inputs, err := listSinkInputs()
	if err != nil {
		log.Printf("audio: %v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	running := map[string]string{}
	want := map[string]string{} // sink input -> app
	for _, in := range inputs {
		// Our own alert sounds stay on your speakers only, and loopbacks
		// replay audio some other stream already carries.
		if strings.Contains(in.driver, "module-loopback") || in.name == AlertClient {
			continue
		}
		key := in.binary
		if key == "" {
			key = in.name
		}
		key = Normalize(key)
		if key == "" {
			continue
		}
		display := in.name
		if display == "" {
			display = key
		}
		running[key] = display
		if m.active && m.policy.Allows(key) {
			want[in.id] = display
		}
	}
	m.apps = buildStatus(m.policy, running)

	for id, cancel := range m.captures {
		if _, ok := want[id]; !ok {
			cancel()
			delete(m.captures, id)
		}
	}
	for id, display := range want {
		if _, ok := m.captures[id]; ok {
			continue
		}
		if t, ok := m.failed[id]; ok && time.Since(t) < 30*time.Second {
			continue
		}
		cctx, cancel := context.WithCancel(ctx)
		m.captures[id] = cancel
		src := m.mixer.Add(id)
		log.Printf("audio: sharing %s (stream %s)", display, id)
		go func() {
			err := captureStream(cctx, id, src)
			m.mixer.Remove(id)
			if err != nil && cctx.Err() == nil {
				log.Printf("audio: capture of %s (stream %s) stopped: %v", display, id, err)
				m.mu.Lock()
				m.failed[id] = time.Now()
				if c, ok := m.captures[id]; ok {
					c()
					delete(m.captures, id)
				}
				m.mu.Unlock()
			}
		}()
	}
}

// captureStream records one sink input, as the mixer's format, until ctx
// ends or the stream goes away.
func captureStream(ctx context.Context, id string, src *Source) error {
	cmd := platform.Command(ctx, "parec", "--monitor-stream="+id, "--raw", "--format=s16le",
		"--rate="+strconv.Itoa(MixRate), "--channels="+strconv.Itoa(MixChannels),
		"--latency-msec=20", "--client-name=Movie night capture")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	buf := make([]byte, bytesFor(20*time.Millisecond))
	carry := 0 // bytes of a partial frame kept for the next read
	for {
		n, err := out.Read(buf[carry:])
		n += carry
		whole := n - n%frameBytes
		if whole > 0 {
			src.Push(append([]byte(nil), buf[:whole]...))
		}
		carry = copy(buf, buf[whole:n])
		if err != nil {
			break
		}
	}
	err = cmd.Wait()
	// "Entity killed": the app closed its stream, which is how a capture
	// normally ends.
	if msg := strings.TrimSpace(stderr.String()); err != nil && !strings.Contains(msg, "Entity killed") {
		return fmt.Errorf("%v %s", err, msg)
	}
	return nil
}

func listSinkInputs() ([]sinkInput, error) {
	out, err := exec.Command("pactl", "list", "sink-inputs").Output()
	if err != nil {
		return nil, fmt.Errorf("pactl list sink-inputs: %w", err)
	}
	var list []sinkInput
	var cur *sinkInput
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "Sink Input #"):
			list = append(list, sinkInput{id: strings.TrimPrefix(line, "Sink Input #")})
			cur = &list[len(list)-1]
		case cur == nil:
		case strings.HasPrefix(line, "Driver: "):
			cur.driver = strings.TrimPrefix(line, "Driver: ")
		case strings.HasPrefix(line, "application.process.binary = "):
			cur.binary = unquote(strings.TrimPrefix(line, "application.process.binary = "))
		case strings.HasPrefix(line, "application.name = "):
			cur.name = unquote(strings.TrimPrefix(line, "application.name = "))
		}
	}
	return list, nil
}

func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return strings.Trim(s, `"`)
}

// Close has nothing to undo: Run's captures end with its context.
func (m *Manager) Close() {}

func pactl(args ...string) (string, error) {
	out, err := exec.Command("pactl", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("pactl %s: %v %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}
