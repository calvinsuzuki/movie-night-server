//go:build !windows && !linux

package audio

import (
	"context"
	"io"
)

// Elsewhere (macOS) all system audio is recorded from a loopback device such
// as BlackHole; per-app selection isn't supported.
type Manager struct{ policy Policy }

func NewManager(p Policy) (*Manager, error) { return &Manager{policy: p}, nil }

func (m *Manager) SetPolicy(p Policy)      { m.policy = p }
func (m *Manager) SetActive(bool)          {}
func (m *Manager) Apps() []AppStatus       { return nil }
func (m *Manager) Run(ctx context.Context) { <-ctx.Done() }
func (m *Manager) Close()                  {}

func (m *Manager) FFmpegInput() ([]string, func(ctx context.Context, w io.Writer) error) {
	return []string{"-f", "avfoundation", "-i", ":BlackHole 2ch"}, nil
}
