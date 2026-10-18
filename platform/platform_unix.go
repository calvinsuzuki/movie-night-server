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
	"syscall"
)

func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
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
