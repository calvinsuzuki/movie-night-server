// Package tunnel gives the local web server a public HTTPS address through a
// Cloudflare quick tunnel (no account, no open ports). Only the viewer page
// and the WebRTC handshake go through it; video flows peer-to-peer.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"movienight/platform"
)

var urlRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Find locates cloudflared: next to the executable (the installers ship it),
// in /usr/lib/movienight, then PATH.
func Find() (string, error) {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name = "cloudflared.exe"
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if runtime.GOOS == "linux" {
		dirs = append(dirs, "/usr/lib/movienight")
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", errors.New("cloudflared was not found; reinstall Movie night")
}

// Tunnel runs cloudflared for as long as its context lives, restarting it if
// it dies. Each run gets a new random address.
type Tunnel struct {
	bin, local string

	mu  sync.Mutex
	url string

	// OnURL is called with the public address whenever it becomes usable,
	// and with "" when it goes away.
	OnURL func(string)
}

func New(bin, localURL string) *Tunnel {
	return &Tunnel{bin: bin, local: localURL}
}

// URL is the current public address, or "" while connecting.
func (t *Tunnel) URL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.url
}

// Run blocks until ctx is cancelled.
func (t *Tunnel) Run(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := t.once(ctx)
		t.setURL("")
		if ctx.Err() != nil {
			return
		}
		log.Printf("tunnel: %v; restarting", err)
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (t *Tunnel) once(ctx context.Context) error {
	// An empty config keeps a user's own ~/.cloudflared/config.yml from
	// turning this into a named tunnel.
	cfg := filepath.Join(os.TempDir(), "movienight-cloudflared.yml")
	os.WriteFile(cfg, []byte("no-autoupdate: true\n"), 0o600)
	cmd := platform.Command(ctx, t.bin, "tunnel", "--no-autoupdate", "--config", cfg, "--url", t.local)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Printf("tunnel: starting cloudflared for %s", t.local)

	found := ""
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		if found == "" {
			if u := urlRe.FindString(line); u != "" {
				found = u
				log.Printf("tunnel: address %s", u)
			}
		}
		// The address works once Cloudflare has a connection registered.
		if found != "" && strings.Contains(line, "Registered tunnel connection") && t.URL() == "" {
			t.setURL(found)
		}
		if strings.Contains(line, " ERR ") || strings.Contains(line, "failed") {
			log.Printf("cloudflared: %s", line)
		}
	}
	err = cmd.Wait()
	if err == nil {
		err = fmt.Errorf("cloudflared exited")
	}
	return err
}

func (t *Tunnel) setURL(u string) {
	t.mu.Lock()
	changed := t.url != u
	t.url = u
	t.mu.Unlock()
	if changed && t.OnURL != nil {
		t.OnURL(u)
	}
}
