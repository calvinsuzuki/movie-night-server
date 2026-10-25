//go:build !windows

package ui

import "time"

// Supported reports whether this system gets Movie night's own menu and
// overlay; elsewhere the tray keeps the system menu.
func Supported() bool { return false }

func ShowMenu(items func() []Item)               {}
func RefreshMenu()                               {}
func SetStatus(s Status)                         {}
func Bubble(emoji, text string, d time.Duration) {}
func Toast(emoji, text string, d time.Duration)  {}
func Identify(marks []Mark)                      {}
