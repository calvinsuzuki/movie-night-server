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
)

// On Linux allowed apps are moved into a "movienight" null sink. A loopback
// plays that sink on the real speakers so the sharer still hears everything,
// and ffmpeg records the sink's monitor. Blocked/unselected apps stay on the
// normal output and never reach the stream. Works with PulseAudio and
// PipeWire's pulse server (Ubuntu 22.04+).
const sinkName = "movienight"

type Manager struct {
	mu        sync.Mutex
	policy    Policy
	apps      []AppStatus
	sinkIdx   string
	modules   []string
	loopModID string
	kick      chan struct{}
}

func NewManager(p Policy) (*Manager, error) {
	if _, err := exec.LookPath("pactl"); err != nil {
		return nil, fmt.Errorf("pactl not found (install pulseaudio-utils)")
	}
	m := &Manager{policy: p, kick: make(chan struct{}, 1)}
	m.unloadStale()
	sink, err := pactl("load-module", "module-null-sink", "sink_name="+sinkName,
		"sink_properties=device.description=Movie-night")
	if err != nil {
		return nil, fmt.Errorf("create sink: %w", err)
	}
	m.modules = append(m.modules, sink)
	loop, err := pactl("load-module", "module-loopback", "source="+sinkName+".monitor",
		"latency_msec=20", "source_dont_move=true")
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("create loopback: %w", err)
	}
	m.modules = append(m.modules, loop)
	m.loopModID = loop
	out, _ := pactl("list", "short", "sinks")
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == sinkName {
			m.sinkIdx = f[0]
		}
	}
	return m, nil
}

// unloadStale removes modules left behind by a previous crash.
func (m *Manager) unloadStale() {
	out, _ := pactl("list", "short", "modules")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sink_name="+sinkName) || strings.Contains(line, "source="+sinkName+".monitor") {
			if f := strings.Fields(line); len(f) > 0 {
				pactl("unload-module", f[0])
			}
		}
	}
}

func (m *Manager) SetPolicy(p Policy) {
	m.mu.Lock()
	m.policy = p
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// SetActive is a no-op: routing is kept even while paused so audio doesn't
// jump between outputs.
func (m *Manager) SetActive(bool) {}

func (m *Manager) Apps() []AppStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AppStatus(nil), m.apps...)
}

func (m *Manager) FFmpegInput() ([]string, func(ctx context.Context, w io.Writer) error) {
	return []string{"-f", "pulse", "-fragment_size", "3840", "-i", sinkName + ".monitor"}, nil
}

func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		m.reconcile()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

type sinkInput struct {
	id, sink, module, driver, binary, name string
}

func (m *Manager) reconcile() {
	inputs, err := listSinkInputs()
	if err != nil {
		log.Printf("audio: %v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	running := map[string]string{}
	for _, in := range inputs {
		if in.module == m.loopModID || strings.Contains(in.driver, "module-loopback") {
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
		onOurs := in.sink == m.sinkIdx
		if allow := m.policy.Allows(key); allow && !onOurs {
			pactl("move-sink-input", in.id, sinkName)
		} else if !allow && onOurs {
			pactl("move-sink-input", in.id, "@DEFAULT_SINK@")
		}
	}
	m.apps = buildStatus(m.policy, running)
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
		case strings.HasPrefix(line, "Sink: "):
			cur.sink = strings.TrimPrefix(line, "Sink: ")
		case strings.HasPrefix(line, "Owner Module: "):
			cur.module = strings.TrimPrefix(line, "Owner Module: ")
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

// Close moves every app back to the normal output and removes our modules.
func (m *Manager) Close() {
	if inputs, err := listSinkInputs(); err == nil {
		for _, in := range inputs {
			if in.sink == m.sinkIdx && in.module != m.loopModID {
				pactl("move-sink-input", in.id, "@DEFAULT_SINK@")
			}
		}
	}
	for i := len(m.modules) - 1; i >= 0; i-- {
		pactl("unload-module", m.modules[i])
	}
	m.modules = nil
}

func pactl(args ...string) (string, error) {
	out, err := exec.Command("pactl", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("pactl %s: %v %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}
