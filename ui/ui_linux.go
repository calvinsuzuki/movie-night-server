//go:build linux

package ui

import (
	"os/exec"
	"strconv"
	"time"
)

// No overlay on Linux: X11 can't leave a window out of the capture, so
// friends would see it.
func SetStatus(s Status)                         {}
func Bubble(emoji, text string, d time.Duration) {}
func Identify(marks []Mark)                      {}

// Toast is a desktop notification where notify-send exists. Unlike the
// Windows one it isn't left out of the capture, so it's kept for news that
// matters before anyone watches.
func Toast(emoji, text string, d time.Duration) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	go exec.Command("notify-send", "-i", "movienight", "-t", strconv.Itoa(int(d.Milliseconds())),
		"Movie night", emoji+" "+text).Run()
}
