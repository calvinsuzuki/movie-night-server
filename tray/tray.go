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
	"movienight/stream"
)

// systray menus are static, so items are reserved up front and hidden.
const (
	appSlots     = 16 // audio apps
	requestSlots = 5  // = the lobby's max waiting requests
	viewerSlots  = 10
)

type menu struct {
	a       *app.App
	logPath string

	status, copyLink, openPage, share, pause     *systray.MenuItem
	waitingHeader                                *systray.MenuItem
	allowItems, denyItems                        []*systray.MenuItem
	watching, nobody                             *systray.MenuItem
	viewerItems                                  []*systray.MenuItem
	monitors                                     []*systray.MenuItem
	presets                                      []*systray.MenuItem
	bitrateAuto                                  *systray.MenuItem
	bitrates                                     []*systray.MenuItem
	shareSound, modeAll, modeList, noApps        *systray.MenuItem
	appItems                                     []*systray.MenuItem
	netInternet, netTailscale, approval, hideIP  *systray.MenuItem
	relay                                        *systray.MenuItem
	blockedInfo, autostart, guide, openLog, quit *systray.MenuItem

	mu       sync.Mutex
	appNames []string          // which app each slot currently shows
	tickets  []string          // which request each slot shows
	popups   map[string]func() // open "wants to watch" popups by ticket, with their close func
	viewers  []string          // which viewer each slot shows
	icon     int
}

// Run blocks until the user quits from the menu.
func Run(a *app.App, logPath string) {
	m := &menu{a: a, logPath: logPath, icon: -1, popups: map[string]func(){}}
	a.OnRequest = m.knock
	systray.Run(m.build, func() {})
}

func (m *menu) build() {
	systray.SetIcon(assets.Tray(assets.TrayPaused))
	systray.SetTitle("")
	systray.SetTooltip(platform.AppName)

	m.status = systray.AddMenuItem(platform.AppName, "")
	m.status.Disable()
	m.copyLink = systray.AddMenuItem("Copy invite link", "Send this link to your friends")
	m.openPage = systray.AddMenuItem("Open viewer page", "")
	m.share = systray.AddMenuItem("Start sharing", "")
	m.pause = systray.AddMenuItem("Pause picture", "Freeze the picture and sound; friends stay connected")

	systray.AddSeparator()
	m.waitingHeader = systray.AddMenuItem("Waiting to join:", "")
	m.waitingHeader.Disable()
	for i := 0; i < requestSlots; i++ {
		allow := systray.AddMenuItem("", "Let them watch")
		deny := systray.AddMenuItem("", "Turn them away")
		allow.Hide()
		deny.Hide()
		m.allowItems = append(m.allowItems, allow)
		m.denyItems = append(m.denyItems, deny)
		m.onClick(allow, func() { m.withTicket(i, m.a.Allow) })
		m.onClick(deny, func() { m.withTicket(i, m.a.Deny) })
	}
	m.watching = systray.AddMenuItem("Watching", "Click a name to remove them")
	m.nobody = m.watching.AddSubMenuItem("Nobody yet", "")
	m.nobody.Disable()
	for i := 0; i < viewerSlots; i++ {
		item := m.watching.AddSubMenuItem("", "Disconnect them; they'd have to ask again")
		item.Hide()
		m.viewerItems = append(m.viewerItems, item)
		m.onClick(item, func() {
			m.mu.Lock()
			id := ""
			if i < len(m.viewers) {
				id = m.viewers[i]
			}
			m.mu.Unlock()
			if id != "" {
				m.a.Kick(id)
			}
		})
	}

	systray.AddSeparator()
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

	network := systray.AddMenuItem("Network", "How friends reach you")
	m.netInternet = network.AddSubMenuItemCheckbox("Internet link (anyone you let in)", "A private link through Cloudflare; video goes directly to each friend", false)
	m.onClick(m.netInternet, func() { m.a.SetNetwork(app.NetInternet) })
	m.netTailscale = network.AddSubMenuItemCheckbox("Tailscale only", "Only devices on your Tailscale network", false)
	m.onClick(m.netTailscale, func() { m.a.SetNetwork(app.NetTailscale) })
	network.AddSeparator()
	m.approval = network.AddSubMenuItemCheckbox("Ask me before anyone joins", "Off: anyone with the link gets in", true)
	m.onClick(m.approval, func() { m.a.SetRequireApproval(!m.a.Config().RequireApproval) })
	m.hideIP = network.AddSubMenuItemCheckbox("Hide my IP (relay only)", "Video goes through your relay server, so friends never see your address", false)
	m.onClick(m.hideIP, func() { m.a.SetHideIP(!m.a.Config().HideIP) })
	m.relay = network.AddSubMenuItem("Relay server settings…", "Opens config.json; used from the next Start sharing")
	m.onClick(m.relay, func() { platform.Open(m.a.ConfigPath()) })

	systray.AddSeparator()
	m.autostart = systray.AddMenuItemCheckbox(platform.AutostartLabel(), "", platform.AutostartEnabled())
	m.guide = systray.AddMenuItem("How it works", "Inviting friends, the relay, Tailscale mode")
	m.openLog = systray.AddMenuItem("Open log", m.logPath)
	m.quit = systray.AddMenuItem("Quit "+platform.AppName, "")

	m.onClick(m.copyLink, func() {
		if err := platform.CopyText(m.a.URL()); err != nil {
			platform.Alert("Couldn't copy the link: " + err.Error() + "\n\n" + m.a.URL())
			return
		}
		m.copyLink.SetTitle("Copied ✓")
		time.AfterFunc(2*time.Second, func() { m.copyLink.SetTitle("Copy invite link") })
	})
	m.onClick(m.openPage, func() { platform.Open(m.a.URL()) })
	m.onClick(m.share, func() {
		if m.a.Sharing() {
			m.a.StopSharing()
		} else {
			m.a.StartSharing()
		}
	})
	m.onClick(m.pause, func() {
		if m.a.State().Paused {
			m.a.Resume()
		} else {
			m.a.Pause()
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

// knock pops up "X wants to watch — let them in?" with a sound. The menu's
// Allow/Deny items work too; whichever is used first wins.
func (m *menu) knock(r stream.Request) {
	platform.PlaySound(assets.KnockSound)
	answer, dismiss := platform.Ask(platform.AppName, fmt.Sprintf("“%s” wants to watch the movie.\n\nLet them in?", r.Name))
	m.mu.Lock()
	m.popups[r.Ticket] = dismiss
	m.mu.Unlock()
	go func() {
		if yes, ok := <-answer; ok {
			if yes {
				m.a.Allow(r.Ticket)
			} else {
				m.a.Deny(r.Ticket)
			}
		}
		m.mu.Lock()
		delete(m.popups, r.Ticket)
		m.mu.Unlock()
		m.refresh()
	}()
}

func (m *menu) withTicket(i int, fn func(string)) {
	m.mu.Lock()
	t := ""
	if i < len(m.tickets) {
		t = m.tickets[i]
	}
	m.mu.Unlock()
	if t != "" {
		fn(t)
	}
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

func setEnabled(item *systray.MenuItem, on bool) {
	if on {
		item.Enable()
	} else {
		item.Disable()
	}
}

func setVisible(item *systray.MenuItem, on bool) {
	if on {
		item.Show()
	} else {
		item.Hide()
	}
}

func (m *menu) refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.a.State()
	cfg := st.Config

	watching := fmt.Sprintf("%d watching", len(st.Viewers))
	switch {
	case st.Status != "":
		m.status.SetTitle("⚠ " + st.Status)
	case !st.Sharing:
		m.status.SetTitle("Not sharing")
	case st.URL == "":
		m.status.SetTitle("Creating your link…")
	case st.Paused:
		m.status.SetTitle("❚❚ Paused · " + watching)
	default:
		m.status.SetTitle("● Sharing · " + watching)
	}
	tip := platform.AppName + " — not sharing"
	if st.Sharing {
		tip = platform.AppName + " — " + watching
	}
	if len(st.Requests) > 0 {
		tip += fmt.Sprintf(", %d waiting", len(st.Requests))
	}
	systray.SetTooltip(tip)

	icon := assets.TrayPaused
	switch {
	case len(st.Requests) > 0:
		icon = assets.TrayRequest
	case st.Sharing && !st.Paused:
		icon = assets.TrayNormal
	}
	if icon != m.icon {
		systray.SetIcon(assets.Tray(icon))
		m.icon = icon
	}

	setEnabled(m.copyLink, st.URL != "")
	setEnabled(m.openPage, st.URL != "")
	if st.Sharing {
		m.share.SetTitle("Stop sharing (disconnects everyone)")
	} else {
		m.share.SetTitle("Start sharing")
	}
	setVisible(m.pause, st.Sharing)
	if st.Paused {
		m.pause.SetTitle("Resume picture")
	} else {
		m.pause.SetTitle("Pause picture")
	}

	// Waiting requests; close popups for requests answered elsewhere or expired.
	pending := map[string]bool{}
	for _, r := range st.Requests {
		pending[r.Ticket] = true
	}
	for t, dismiss := range m.popups {
		if !pending[t] {
			dismiss()
			delete(m.popups, t)
		}
	}
	m.tickets = m.tickets[:0]
	setVisible(m.waitingHeader, len(st.Requests) > 0)
	for i := range m.allowItems {
		if i < len(st.Requests) {
			r := st.Requests[i]
			m.allowItems[i].SetTitle(fmt.Sprintf("✓ Let “%s” in", r.Name))
			m.denyItems[i].SetTitle(fmt.Sprintf("    ✗ Deny “%s”", r.Name))
			m.allowItems[i].Show()
			m.denyItems[i].Show()
			m.tickets = append(m.tickets, r.Ticket)
		} else {
			m.allowItems[i].Hide()
			m.denyItems[i].Hide()
		}
	}

	// Viewers.
	m.viewers = m.viewers[:0]
	m.watching.SetTitle(fmt.Sprintf("Watching (%d)", len(st.Viewers)))
	setVisible(m.nobody, len(st.Viewers) == 0)
	for i, item := range m.viewerItems {
		if i < len(st.Viewers) {
			v := st.Viewers[i]
			item.SetTitle(fmt.Sprintf("%s · %s — Kick", v.Name, since(v.Since)))
			item.Show()
			m.viewers = append(m.viewers, v.ID)
		} else {
			item.Hide()
		}
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
		setEnabled(item, !ap.Blocked && cfg.AudioMode == audio.ModeWhitelist && cfg.ShareAudio)
		item.Show()
		m.appNames = append(m.appNames, ap.Name)
		shown++
	}
	for i := shown; i < len(m.appItems); i++ {
		m.appItems[i].Hide()
	}
	setVisible(m.noApps, shown == 0)

	setChecked(m.netInternet, cfg.Network == app.NetInternet)
	setChecked(m.netTailscale, cfg.Network == app.NetTailscale)
	setChecked(m.approval, cfg.RequireApproval)
	setChecked(m.hideIP, cfg.RelayOnly())
	setEnabled(m.hideIP, cfg.Network == app.NetInternet && cfg.TURN.Configured())
	setEnabled(m.relay, cfg.Network == app.NetInternet)

	setChecked(m.autostart, platform.AutostartEnabled())
}

func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero() || d < time.Minute:
		return "just joined"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02d", int(d.Hours()), int(d.Minutes())%60)
	}
}

// OpenGuide shows the "How it works" page in the default browser.
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
