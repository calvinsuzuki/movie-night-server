// Package tray is Movie night's only UI: a menu on the taskbar tray icon
// (Windows notification area / Ubuntu AppIndicator).
package tray

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"

	"movienight/app"
	"movienight/assets"
	"movienight/audio"
	"movienight/platform"
)

const appSlots = 16 // menu items reserved for audio apps (systray menus are static)

type menu struct {
	a       *app.App
	logPath string

	status, link, copyLink, openPage, toggle *systray.MenuItem
	monitors                                 []*systray.MenuItem
	presets                                  []*systray.MenuItem
	bitrateAuto                              *systray.MenuItem
	bitrates                                 []*systray.MenuItem
	shareSound, modeAll, modeList, noApps    *systray.MenuItem
	appItems                                 []*systray.MenuItem
	blockedInfo, autostart, openLog, quit    *systray.MenuItem
	guide                                    *systray.MenuItem

	mu       sync.Mutex
	appNames []string // which app each slot currently shows
	paused   *bool
}

// Run blocks until the user quits from the menu.
func Run(a *app.App, logPath string) {
	m := &menu{a: a, logPath: logPath}
	systray.Run(m.build, func() {})
}

func (m *menu) build() {
	systray.SetIcon(assets.Tray(false))
	systray.SetTitle("")
	systray.SetTooltip(platform.AppName)

	m.status = systray.AddMenuItem(platform.AppName, "")
	m.status.Disable()
	m.link = systray.AddMenuItem("", "Viewer link")
	m.link.Disable()
	m.copyLink = systray.AddMenuItem("Copy viewer link", "Send this link to your friends")
	m.openPage = systray.AddMenuItem("Open viewer page", "")
	systray.AddSeparator()
	m.toggle = systray.AddMenuItem("Pause sharing", "")

	screen := systray.AddMenuItem("Screen", "Which monitor to share")
	for i, mon := range m.a.Monitors {
		m.monitors = append(m.monitors, screen.AddSubMenuItemCheckbox(mon.String(), "", false))
		m.onClick(m.monitors[i], func() { m.a.SetMonitor(i) })
	}

	quality := systray.AddMenuItem("Quality", "Resolution and bitrate")
	for _, p := range app.Presets {
		item := quality.AddSubMenuItemCheckbox(fmt.Sprintf("%s  (%s)", p.Label, kbps(p.Kbps)), "", false)
		m.presets = append(m.presets, item)
		m.onClick(item, func() { m.a.SetQuality(p.ID) })
	}
	quality.AddSeparator()
	adv := quality.AddSubMenuItem("Bitrate (advanced)", "Override the preset's bitrate")
	m.bitrateAuto = adv.AddSubMenuItemCheckbox("Automatic (preset default)", "", false)
	m.onClick(m.bitrateAuto, func() { m.a.SetBitrate(0) })
	adv.AddSeparator()
	for _, b := range app.Bitrates {
		item := adv.AddSubMenuItemCheckbox(kbps(b), "", false)
		m.bitrates = append(m.bitrates, item)
		m.onClick(item, func() { m.a.SetBitrate(b) })
	}

	sound := systray.AddMenuItem("Sound", "Which apps your friends hear")
	m.shareSound = sound.AddSubMenuItemCheckbox("Share sound", "", true)
	m.onClick(m.shareSound, func() { m.a.SetShareAudio(!m.a.Config().ShareAudio) })
	sound.AddSeparator()
	m.modeAll = sound.AddSubMenuItemCheckbox("All apps (except voice chat)", "", false)
	m.onClick(m.modeAll, func() { m.a.SetAudioMode(audio.ModeAll) })
	m.modeList = sound.AddSubMenuItemCheckbox("Only the apps checked below", "Whitelist", false)
	m.onClick(m.modeList, func() { m.a.SetAudioMode(audio.ModeWhitelist) })
	sound.AddSeparator()
	m.noApps = sound.AddSubMenuItem("No apps are playing sound", "")
	m.noApps.Disable()
	for i := 0; i < appSlots; i++ {
		item := sound.AddSubMenuItemCheckbox("", "", false)
		item.Hide()
		m.appItems = append(m.appItems, item)
		m.onClick(item, func() {
			m.mu.Lock()
			name := ""
			if i < len(m.appNames) {
				name = m.appNames[i]
			}
			m.mu.Unlock()
			if name != "" {
				m.a.ToggleApp(name)
			}
		})
	}
	sound.AddSeparator()
	m.blockedInfo = sound.AddSubMenuItem("Discord and TeamSpeak are never shared", "Voice chat would echo back to your friends")
	m.blockedInfo.Disable()

	systray.AddSeparator()
	m.autostart = systray.AddMenuItemCheckbox(platform.AutostartLabel(), "", platform.AutostartEnabled())
	m.guide = systray.AddMenuItem("Tailscale guide", "How to set up Tailscale for you and your friends")
	m.openLog = systray.AddMenuItem("Open log", m.logPath)
	m.quit = systray.AddMenuItem("Quit "+platform.AppName, "")

	m.onClick(m.copyLink, func() {
		if err := platform.CopyText(m.a.URL()); err != nil {
			platform.Alert("Couldn't copy the link: " + err.Error() + "\n\n" + m.a.URL())
			return
		}
		m.copyLink.SetTitle("Copied ✓")
		time.AfterFunc(2*time.Second, func() { m.copyLink.SetTitle("Copy viewer link") })
	})
	m.onClick(m.openPage, func() { platform.Open(m.a.URL()) })
	m.onClick(m.toggle, func() {
		if m.a.Sharing() {
			m.a.StopSharing()
		} else {
			m.a.StartSharing()
		}
	})
	m.onClick(m.autostart, func() {
		on := !platform.AutostartEnabled()
		if err := platform.SetAutostart(on); err != nil {
			log.Printf("autostart: %v", err)
		}
		m.refresh()
	})
	m.onClick(m.guide, OpenGuide)
	m.onClick(m.openLog, func() { platform.Open(filepath.Clean(m.logPath)) })
	m.onClick(m.quit, systray.Quit)

	m.refresh()
	go func() {
		t := time.NewTicker(2 * time.Second)
		for {
			select {
			case <-t.C:
			case <-m.a.Changed():
			}
			m.refresh()
		}
	}()
}

func (m *menu) onClick(item *systray.MenuItem, fn func()) {
	go func() {
		for range item.ClickedCh {
			fn()
			m.refresh()
		}
	}()
}

func setChecked(item *systray.MenuItem, on bool) {
	if on != item.Checked() {
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}

func (m *menu) refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.a.State()
	cfg := st.Config

	viewers := fmt.Sprintf("%d watching", st.Viewers)
	if st.Viewers == 1 {
		viewers = "1 watching"
	}
	switch {
	case st.Status != "":
		m.status.SetTitle("⚠ " + st.Status)
	case st.Sharing:
		m.status.SetTitle("● Sharing · " + viewers)
	default:
		m.status.SetTitle("❚❚ Paused · " + viewers)
	}
	systray.SetTooltip(fmt.Sprintf("%s — %s", platform.AppName, map[bool]string{true: "sharing", false: "paused"}[st.Sharing]+", "+viewers))
	if m.paused == nil || *m.paused == st.Sharing {
		systray.SetIcon(assets.Tray(!st.Sharing))
		p := !st.Sharing
		m.paused = &p
	}
	if st.URL == "" {
		m.link.SetTitle("Viewer link appears once Tailscale is connected")
		m.copyLink.Disable()
		m.openPage.Disable()
	} else {
		m.link.SetTitle(st.URL)
		m.copyLink.Enable()
		m.openPage.Enable()
	}
	if st.Sharing {
		m.toggle.SetTitle("Pause sharing")
	} else {
		m.toggle.SetTitle("Start sharing")
	}

	for i, item := range m.monitors {
		setChecked(item, i == cfg.Monitor)
	}
	for i, item := range m.presets {
		setChecked(item, app.Presets[i].ID == cfg.Quality)
	}
	setChecked(m.bitrateAuto, cfg.BitrateKbps == 0)
	for i, item := range m.bitrates {
		setChecked(item, app.Bitrates[i] == cfg.BitrateKbps)
	}

	setChecked(m.shareSound, cfg.ShareAudio)
	setChecked(m.modeAll, cfg.AudioMode != audio.ModeWhitelist)
	setChecked(m.modeList, cfg.AudioMode == audio.ModeWhitelist)

	m.appNames = m.appNames[:0]
	shown := 0
	for _, ap := range st.Apps {
		if shown >= len(m.appItems) {
			break
		}
		item := m.appItems[shown]
		title := ap.Display
		switch {
		case ap.Blocked:
			title += " — voice chat, never shared"
		case !ap.Running:
			title += " (not playing)"
		}
		item.SetTitle(title)
		// The check shows whether friends hear the app (or, in whitelist mode, whether
		// it's whitelisted); only whitelist mode lets you toggle it.
		if cfg.AudioMode == audio.ModeWhitelist {
			setChecked(item, !ap.Blocked && containsApp(cfg.Whitelist, ap.Name))
		} else {
			setChecked(item, ap.Sharing)
		}
		if ap.Blocked || cfg.AudioMode != audio.ModeWhitelist || !cfg.ShareAudio {
			item.Disable()
		} else {
			item.Enable()
		}
		item.Show()
		m.appNames = append(m.appNames, ap.Name)
		shown++
	}
	for i := shown; i < len(m.appItems); i++ {
		m.appItems[i].Hide()
	}
	if shown == 0 {
		m.noApps.Show()
	} else {
		m.noApps.Hide()
	}

	setChecked(m.autostart, platform.AutostartEnabled())
}

// OpenGuide shows the Tailscale how-to page in the default browser.
func OpenGuide() {
	path, err := app.WriteGuide(platform.ConfigDir())
	if err != nil {
		log.Printf("guide: %v", err)
		return
	}
	platform.Open(path)
}

func containsApp(list []string, name string) bool {
	for _, w := range list {
		if audio.Normalize(w) == name {
			return true
		}
	}
	return false
}

func kbps(k int) string {
	if k >= 1000 {
		return fmt.Sprintf("%g Mbps", float64(k)/1000)
	}
	return fmt.Sprintf("%d kbps", k)
}
