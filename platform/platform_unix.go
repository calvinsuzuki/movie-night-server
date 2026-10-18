//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// Command runs a helper program (ffmpeg, cloudflared) that dies with us.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = childAttr()
	return cmd
}

func Open(target string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, target).Start()
}

func CopyText(s string) error {
	candidates := [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	if runtime.GOOS == "darwin" {
		candidates = [][]string{{"pbcopy"}}
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return errors.New("no clipboard tool found (install xclip)")
}

func autostartFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "autostart", "movienight.desktop")
}

func AutostartEnabled() bool {
	_, err := os.Stat(autostartFile())
	return err == nil
}

func SetAutostart(on bool) error {
	if runtime.GOOS != "linux" {
		return errors.New("autostart is only supported on Linux and Windows")
	}
	path := autostartFile()
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=%s\nIcon=movienight\nX-GNOME-Autostart-enabled=true\n", AppName, exe)
	return os.WriteFile(path, []byte(desktop), 0o644)
}

var lockFile *os.File

// SingleInstance returns false if Movie night is already running.
func SingleInstance() bool {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(dir, "movienight.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f // held until exit
	return true
}

func Alert(msg string) {
	if _, err := exec.LookPath("zenity"); err == nil {
		exec.Command("zenity", "--info", "--title="+AppName, "--no-wrap", "--text="+msg).Run()
		return
	}
	if _, err := exec.LookPath("notify-send"); err == nil {
		exec.Command("notify-send", "-i", "movienight", AppName, msg).Run()
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// Ask shows a "Let in / Deny" popup (zenity) without blocking. answer gets
// true or false when the user clicks; dismiss closes it without an answer.
// Without zenity it falls back to a plain notification.
func Ask(title, msg string) (answer <-chan bool, dismiss func()) {
	ch := make(chan bool, 1)
	if _, err := exec.LookPath("zenity"); err != nil {
		go exec.Command("notify-send", "-i", "movienight", "-u", "critical", title, msg).Run()
		return ch, func() {}
	}
	cmd := exec.Command("zenity", "--question", "--title="+title, "--text="+msg, "--no-wrap",
		"--ok-label=Let in", "--cancel-label=Deny", "--icon-name=movienight")
	if err := cmd.Start(); err != nil {
		return ch, func() {}
	}
	var dismissed atomic.Bool
	go func() {
		err := cmd.Wait()
		if dismissed.Load() {
			return
		}
		var exit *exec.ExitError
		switch {
		case err == nil:
			ch <- true
		case errors.As(err, &exit) && exit.ExitCode() == 1: // Deny
			ch <- false
		}
	}()
	return ch, func() {
		if !dismissed.Swap(true) {
			cmd.Process.Kill()
		}
	}
}

// alertClient must match audio.AlertClient, so the sound isn't shared.
const alertClient = "Movie night alerts"

var (
	soundMu    sync.Mutex
	soundFiles = map[*byte]string{}
)

// PlaySound plays a WAV file held in memory, without waiting.
func PlaySound(wav []byte) {
	if len(wav) == 0 {
		return
	}
	soundMu.Lock()
	path, ok := soundFiles[&wav[0]]
	if !ok {
		f, err := os.CreateTemp("", "movienight-*.wav")
		if err != nil {
			soundMu.Unlock()
			return
		}
		f.Write(wav)
		f.Close()
		path = f.Name()
		soundFiles[&wav[0]] = path
	}
	soundMu.Unlock()
	switch {
	case runtime.GOOS == "darwin":
		go exec.Command("afplay", path).Run()
	case lookPath("paplay"):
		go exec.Command("paplay", "--client-name="+alertClient, path).Run()
	case lookPath("aplay"):
		go exec.Command("aplay", "-q", path).Run()
	}
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
