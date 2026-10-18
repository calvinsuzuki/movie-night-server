// Movie night streams one monitor plus selected apps' sound to browsers on
// your Tailscale network over WebRTC. It lives in the taskbar tray.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"

	"movienight/app"
	"movienight/platform"
	"movienight/tray"
)

func main() {
	headless := flag.Bool("headless", false, "run without the tray icon (Ctrl+C to stop)")
	listMonitors := flag.Bool("list-monitors", false, "list capturable monitors and exit")
	guide := flag.Bool("guide", false, "open the Tailscale guide and exit (used by the installer)")
	flag.Parse()

	if *guide {
		tray.OpenGuide()
		return
	}

	dir := platform.ConfigDir()
	os.MkdirAll(dir, 0o755)
	logPath := filepath.Join(dir, "movienight.log")
	if f, err := os.Create(logPath); err == nil {
		// File first: in a GUI build stderr is invalid and MultiWriter stops at the first error.
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
	log.SetFlags(log.Ldate | log.Ltime)

	if !*listMonitors && !platform.SingleInstance() {
		platform.Alert("Movie night is already running.\nLook for the popcorn icon in the taskbar tray.")
		return
	}

	ffmpeg, err := findFFmpeg()
	if err != nil {
		log.Print(err)
		platform.Alert(err.Error())
		os.Exit(1)
	}
	log.Printf("using %s", ffmpeg)

	a, err := app.New(ffmpeg, filepath.Join(dir, "config.json"))
	if err != nil {
		log.Print(err)
		platform.Alert("Movie night can't capture the screen:\n" + err.Error())
		os.Exit(1)
	}
	if *listMonitors {
		for i, m := range a.Monitors {
			fmt.Printf("  %d: %s\n", i, m)
		}
		return
	}

	// Nothing works without Tailscale; show how to get it (e.g. a fresh Ubuntu install).
	if !*headless && app.TailscaleState() == app.TailscaleMissing {
		tray.OpenGuide()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx); err != nil {
		log.Print(err)
		platform.Alert("Movie night couldn't start:\n" + err.Error())
		os.Exit(1)
	}

	if *headless {
		sig, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		<-sig.Done()
	} else {
		tray.Run(a, logPath)
	}
	log.Print("shutting down")
	cancel()
	a.Shutdown()
}

// findFFmpeg prefers a copy shipped next to the executable (the Windows
// installer bundles one), then PATH.
func findFFmpeg() (string, error) {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	// Apps started from Explorer may have a stale PATH right after winget installs ffmpeg.
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links", name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "linux" {
		return "", fmt.Errorf("ffmpeg is not installed.\nRun: sudo apt install ffmpeg")
	}
	return "", fmt.Errorf("ffmpeg was not found. Reinstall Movie night or run: winget install Gyan.FFmpeg")
}
