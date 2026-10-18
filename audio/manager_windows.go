//go:build windows

package audio

import (
	"context"
	"io"
	"log"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/go-ole/go-ole"
)

// Manager keeps one process-loopback capture running for every app the
// policy allows, while capturing is active.
type Manager struct {
	mixer *Mixer

	mu       sync.Mutex
	policy   Policy
	active   bool
	apps     []AppStatus
	captures map[uint32]context.CancelFunc
	failed   map[uint32]time.Time
	kick     chan struct{}
}

func NewManager(p Policy) (*Manager, error) {
	return &Manager{
		mixer: NewMixer(), policy: p,
		captures: map[uint32]context.CancelFunc{}, failed: map[uint32]time.Time{},
		kick: make(chan struct{}, 1),
	}, nil
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

// Run polls the audio sessions every two seconds (or when poked) and
// reconciles the running captures.
func (m *Manager) Run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 {
			log.Printf("audio: CoInitializeEx: %v", err)
			return
		}
	}
	defer ole.CoUninitialize()

	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		m.reconcile(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for pid, cancel := range m.captures {
				cancel()
				delete(m.captures, pid)
			}
			m.mu.Unlock()
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	sessions, err := listSessions()
	if err != nil {
		log.Printf("audio: %v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	running := map[string]string{}
	want := map[uint32]string{}
	self := uint32(os.Getpid()) // activating loopback gives us a session too
	for _, s := range sessions {
		if s.pid == self {
			continue
		}
		running[s.name] = s.display
		if m.active && m.policy.Allows(s.name) {
			want[s.pid] = s.display
		}
	}
	m.apps = buildStatus(m.policy, running)

	for pid, cancel := range m.captures {
		if _, ok := want[pid]; !ok {
			cancel()
			delete(m.captures, pid)
		}
	}
	for pid, display := range want {
		if _, ok := m.captures[pid]; ok {
			continue
		}
		if t, ok := m.failed[pid]; ok && time.Since(t) < 30*time.Second {
			continue
		}
		key := strconv.Itoa(int(pid))
		cctx, cancel := context.WithCancel(ctx)
		m.captures[pid] = cancel
		src := m.mixer.Add(key)
		log.Printf("audio: sharing %s (pid %d)", display, pid)
		go func() {
			err := captureProcess(cctx, pid, src)
			m.mixer.Remove(key)
			if err != nil {
				log.Printf("audio: capture of %s (pid %d) failed: %v", display, pid, err)
				m.mu.Lock()
				m.failed[pid] = time.Now()
				if c, ok := m.captures[pid]; ok {
					c()
					delete(m.captures, pid)
				}
				m.mu.Unlock()
			}
		}()
	}
}

func (m *Manager) Close() {}
