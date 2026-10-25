// Package tray is Movie night's home: the popcorn icon in the taskbar tray
// (Windows notification area / Ubuntu AppIndicator) and its menu. On
// Windows the menu, and the overlay on the shared screen, are Movie night's
// own (package ui); elsewhere the menu is the system's.
package tray

import (
	"fmt"
	"log"
	"net"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"

	"movienight/app"
	"movienight/assets"
	"movienight/audio"
	"movienight/capture"
	"movienight/platform"
	"movienight/stream"
	"movienight/ui"
)

// systray menus are static, so items are reserved up front and hidden.
const (
	appSlots     = 16 // audio apps
	requestSlots = 5  // = the lobby's max waiting requests
	viewerSlots  = 10
)

// aloneAfter is how long sharing with nobody watching goes before Movie
// night asks whether to stop.
const aloneAfter = 2 * time.Minute

// aloneGrace is how long that question waits for an answer before the
// stream ends by itself.
const aloneGrace = 3 * time.Minute

// bubbleCorners are where the camera bubble can sit, as the menus name them.
var bubbleCorners = []struct{ id, label string }{
	{capture.BottomRight, "Bottom right"}, {capture.BottomLeft, "Bottom left"},
	{capture.TopRight, "Top right"}, {capture.TopLeft, "Top left"},
}

// addrSlots is how many local addresses the system menu can list.
const addrSlots = 6

type menu struct {
	a       *app.App
	logPath string
	custom  bool // our own menu (ui) instead of the system's

	status, copyLink, openPage, share, pause *systray.MenuItem
	waitingHeader                            *systray.MenuItem
	allowItems, denyItems                    []*systray.MenuItem
	watching, nobody                         *systray.MenuItem
	viewerItems                              []*systray.MenuItem
	monitors                                 []*systray.MenuItem
	cameras, bubbles, corners                []*systray.MenuItem
	bubbleOff                                *systray.MenuItem
	presets                                  []*systray.MenuItem
	bitrates                                 []*systray.MenuItem
	shareSound, modeAll, modeList, noApps    *systray.MenuItem
	appItems                                 []*systray.MenuItem
	netInternet, approval, hideIP            *systray.MenuItem
	addrItems                                []*systray.MenuItem
	relay, askAlone                          *systray.MenuItem
	autostart, guide, openLog, quit          *systray.MenuItem

	mu          sync.Mutex
	appNames    []string          // which app each slot currently shows
	tickets     []string          // which request each slot shows
	popups      map[string]func() // open "wants to watch" popups by ticket, with their close func
	viewers     []string          // which viewer each slot shows
	icon        string
	copiedUntil time.Time     // "Copied ✓" next to Copy invite link
	autoCopied  int           // the share session whose link went to the clipboard on its own
	addrs       []app.Address // which address each network slot shows
	aloneSince  time.Time     // sharing with nobody watching since
	askedAlone  bool
	aloneClose  func() // closes the "streaming alone" popup
}

// Run blocks until the user quits from the menu.
func Run(a *app.App, logPath string) {
	m := &menu{a: a, logPath: logPath, popups: map[string]func(){}}
	a.OnRequest = m.knock
	a.OnThrow = m.thrown
	systray.Run(m.build, func() {})
}

// Quit closes the tray, making Run return.
func Quit() { systray.Quit() }

func (m *menu) build() {
	systray.SetIcon(assets.Tray(assets.TrayIdle))
	systray.SetTitle("")
	systray.SetTooltip(platform.AppName)

	if m.custom = ui.Supported(); m.custom {
		open := func() { ui.ShowMenu(m.items) }
		systray.SetOnTapped(open)
		systray.SetOnSecondaryTapped(open)
	} else {
		m.buildNative()
	}

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

func (m *menu) buildNative() {
	m.status = systray.AddMenuItem(platform.AppName, "Click to copy the invite link")
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
	for i, cam := range m.a.Cameras {
		item := screen.AddSubMenuItemCheckbox("Camera · "+cam.Name, "Share this camera instead of a monitor", false)
		m.cameras = append(m.cameras, item)
		m.onClick(item, func() { m.a.SetCamera(i) })
	}
	if len(m.a.Cameras) > 0 {
		bubble := systray.AddMenuItem("Camera bubble", "A camera in a corner of the shared monitor")
		m.bubbleOff = bubble.AddSubMenuItemCheckbox("Off", "", true)
		m.onClick(m.bubbleOff, func() { m.a.SetBubble(-1) })
		for i, cam := range m.a.Cameras {
			item := bubble.AddSubMenuItemCheckbox(cam.Name, "", false)
			m.bubbles = append(m.bubbles, item)
			m.onClick(item, func() { m.a.SetBubble(i) })
		}
		bubble.AddSeparator()
		for _, c := range bubbleCorners {
			item := bubble.AddSubMenuItemCheckbox(c.label, "", false)
			m.corners = append(m.corners, item)
			m.onClick(item, func() { m.a.SetBubbleCorner(c.id) })
		}
	}

	quality := systray.AddMenuItem("Quality", "Resolution and bitrate")
	for _, p := range app.Presets {
		item := quality.AddSubMenuItemCheckbox(fmt.Sprintf("%s  (%s)", p.Label, kbps(p.Kbps)), "", false)
		m.presets = append(m.presets, item)
		m.onClick(item, func() { m.a.SetQuality(p.ID) })
	}
	quality.AddSeparator()
	adv := quality.AddSubMenuItem("Bitrate", "Until the next preset is picked")
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
	m.modeList = sound.AddSubMenuItemCheckbox("Whitelist apps", "Only the apps checked below", false)
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

	systray.AddSeparator()
	advanced := systray.AddMenuItem("Advanced", "Everything you set once")
	network := advanced.AddSubMenuItem("Network", "How friends reach you")
	m.netInternet = network.AddSubMenuItemCheckbox("Internet link (anyone you let in)", "A private link through Cloudflare; video goes directly to each friend", false)
	m.onClick(m.netInternet, func() { m.a.SetLocal(nil) })
	for i := 0; i < addrSlots; i++ {
		item := network.AddSubMenuItemCheckbox("", "Only this network, until Movie night quits", false)
		item.Hide()
		m.addrItems = append(m.addrItems, item)
		m.onClick(item, func() {
			m.mu.Lock()
			var ip net.IP
			if i < len(m.addrs) {
				ip = m.addrs[i].IP
			}
			m.mu.Unlock()
			if ip != nil {
				m.a.SetLocal(ip)
			}
		})
	}
	network.AddSeparator()
	m.approval = network.AddSubMenuItemCheckbox("Ask me before anyone joins", "Off: anyone with the link gets in", true)
	m.onClick(m.approval, func() { m.a.SetRequireApproval(!m.a.Config().RequireApproval) })
	m.hideIP = network.AddSubMenuItemCheckbox("Hide my IP (relay only)", "Video goes through your relay server, so friends never see your address", false)
	m.onClick(m.hideIP, func() { m.a.SetHideIP(!m.a.Config().HideIP) })
	m.relay = network.AddSubMenuItem("Relay server settings…", "Opens config.json; used from the next Start sharing")
	m.onClick(m.relay, func() { platform.Open(m.a.ConfigPath()) })

	m.askAlone = advanced.AddSubMenuItemCheckbox("Ask to stop when nobody's watching", "After 2 minutes of sharing to nobody", true)
	m.onClick(m.askAlone, func() { m.a.SetAskWhenAlone(!m.a.Config().AskWhenAlone) })
	m.autostart = advanced.AddSubMenuItemCheckbox(platform.AutostartLabel(), "", platform.AutostartEnabled())
	m.guide = advanced.AddSubMenuItem("How it works", "Inviting friends, the relay, local addresses")
	m.openLog = advanced.AddSubMenuItem("Open log", m.logPath)
	systray.AddSeparator()
	m.quit = systray.AddMenuItem("Quit "+platform.AppName, "")

	// While sharing, the status line is the link: a click copies it.
	m.onClick(m.status, func() {
		if m.a.State().URL != "" {
			m.copy()
		}
	})
	m.onClick(m.copyLink, func() {
		if !m.copy() {
			return
		}
		m.copyLink.SetTitle("Copied ✓")
		time.AfterFunc(2*time.Second, func() { m.copyLink.SetTitle("Copy invite link") })
	})
	m.onClick(m.openPage, m.openViewerPage)
	m.onClick(m.share, m.toggleSharing)
	m.onClick(m.pause, m.togglePause)
	m.onClick(m.autostart, m.toggleAutostart)
	m.onClick(m.guide, OpenGuide)
	m.onClick(m.openLog, m.showLog)
	m.onClick(m.quit, systray.Quit)
}

func (m *menu) onClick(item *systray.MenuItem, fn func()) {
	go func() {
		for range item.ClickedCh {
			fn()
			m.refresh()
		}
	}()
}

func (m *menu) toggleSharing() {
	if m.a.Sharing() {
		m.a.StopSharing()
	} else {
		m.a.StartSharing()
	}
}

func (m *menu) togglePause() {
	if m.a.State().Paused {
		m.a.Resume()
	} else {
		m.a.Pause()
	}
}

func (m *menu) toggleAutostart() {
	if err := platform.SetAutostart(!platform.AutostartEnabled()); err != nil {
		log.Printf("autostart: %v", err)
	}
	m.refresh()
}

// openViewerPage opens the stream in the browser as the host's own tab: the
// page fills in their name and gets in without asking.
func (m *menu) openViewerPage() { platform.Open(m.a.URL() + "#host=" + m.a.HostKey()) }

func (m *menu) showLog() { platform.Open(filepath.Clean(m.logPath)) }

// copy puts the invite link on the clipboard, and says so if it can't.
func (m *menu) copy() bool {
	url := m.a.URL()
	if err := copyText(url); err != nil {
		platform.Alert("Couldn't copy the link: " + err.Error() + "\n\n" + url)
		return false
	}
	m.mu.Lock()
	m.copiedUntil = time.Now().Add(2 * time.Second)
	m.mu.Unlock()
	time.AfterFunc(2*time.Second, m.refresh)
	return true
}

// copyText retries for a moment: another app may be holding the clipboard.
func copyText(s string) error {
	var err error
	for try := 0; try < 5; try++ {
		if err = platform.CopyText(s); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// autoCopy puts a new share's link on the clipboard as soon as it works,
// so it's ready to paste in the group chat.
func (m *menu) autoCopy(url string) {
	if err := copyText(url); err != nil {
		log.Printf("couldn't copy the invite link: %v", err)
		return
	}
	log.Print("invite link copied to the clipboard")
	ui.Toast("🔗", "Invite link copied · paste it to your friends", 5*time.Second)
}

// knock pops up "X wants to watch — let them in?" with a sound. The menu's
// Allow/Deny items work too; whichever is used first wins.
func (m *menu) knock(r stream.Request) {
	platform.PlaySound(assets.KnockSound)
	answer, dismiss := platform.Ask(platform.AppName, fmt.Sprintf("“%s” wants to watch the movie. Let them in?", r.Name))
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

// thrown shows a friend's emoji in the overlay, so the sharer sees it too.
func (m *menu) thrown(t stream.Throw) {
	if m.a.Config().ShowOverlay {
		ui.Bubble(t.Emoji, t.Name, ui.BubbleTime)
	}
}

// askIfAlone offers to stop sharing once nobody has watched for a while;
// m.mu must be held.
func (m *menu) askIfAlone(st app.State) {
	if !st.Sharing || st.URL == "" || len(st.Viewers) > 0 || len(st.Requests) > 0 {
		m.aloneSince, m.askedAlone = time.Time{}, false
		if m.aloneClose != nil { // someone came after all
			m.aloneClose()
			m.aloneClose = nil
		}
		return
	}
	if m.aloneSince.IsZero() {
		m.aloneSince = time.Now()
	}
	if m.askedAlone || !st.Config.AskWhenAlone || time.Since(m.aloneSince) < aloneAfter {
		return
	}
	m.askedAlone = true
	log.Print("nobody has watched for 2 minutes: asking whether to stop")
	// No answer within aloneGrace: the sharer has likely walked away.
	answer, dismiss := platform.ChooseTimed(platform.AppName, "You are streaming alone.",
		"End stream", "Keep going", "Don't ask again", aloneGrace)
	m.aloneClose = dismiss
	go func() {
		c, ok := <-answer
		m.mu.Lock()
		m.aloneClose = nil
		m.mu.Unlock()
		switch {
		case !ok:
		case c == 0:
			m.a.StopSharing()
		case c == 2:
			m.a.SetAskWhenAlone(false)
		case c == 3:
			log.Print("nobody watched and nobody answered for 3 minutes: stopping")
			m.a.StopSharing()
		}
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

func watchingText(n int) string { return fmt.Sprintf("%d watching", n) }

// overlayText is the overlay's pill: how many are watching, or why nobody can yet.
func overlayText(st app.State) string {
	n := len(st.Viewers)
	switch {
	case st.URL == "":
		return "Creating your link…"
	case st.Paused:
		return "Paused · " + watchingText(n)
	case n == 0:
		return "Nobody's watching yet"
	}
	return watchingText(n)
}

func overlayTone(st app.State) ui.Tone {
	switch {
	case st.Status != "":
		return ui.ToneBad
	case len(st.Viewers) > 0 && !st.Paused:
		return ui.ToneLive
	}
	return ui.ToneWarn
}

// trayIcon picks the icon for the state: grey until there's a link to
// share, with a badge for what's going on.
func trayIcon(st app.State) string {
	switch {
	case len(st.Requests) > 0:
		return assets.TrayRequest
	case !st.Sharing:
		return assets.TrayIdle
	case st.URL == "":
		return assets.TrayLoading
	case st.Paused:
		return assets.TrayPaused
	case !st.Config.ShareAudio:
		return assets.TrayMuted
	}
	return assets.TrayLive
}

func (m *menu) refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.a.State()
	cfg := st.Config

	tip := platform.AppName + " — not sharing"
	if st.Sharing {
		tip = platform.AppName + " — " + watchingText(len(st.Viewers))
	}
	if len(st.Requests) > 0 {
		tip += fmt.Sprintf(", %d waiting", len(st.Requests))
	}
	systray.SetTooltip(tip)

	icon := trayIcon(st)
	if icon != m.icon {
		systray.SetIcon(assets.Tray(icon))
		m.icon = icon
	}

	// Close popups for requests answered elsewhere or expired.
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

	if st.Sharing && st.URL != "" && m.autoCopied != st.Session {
		m.autoCopied = st.Session
		go m.autoCopy(st.URL)
	}
	m.askIfAlone(st)

	if m.custom {
		mon := m.a.Monitors[cfg.Monitor]
		ui.SetStatus(ui.Status{Visible: st.Sharing && cfg.ShowOverlay, Text: overlayText(st), Tone: overlayTone(st),
			Adapter: mon.Adapter, Output: mon.Output})
		ui.RefreshMenu()
		return
	}
	m.refreshNative(st)
}

// refreshNative updates the system menu's items; m.mu must be held.
func (m *menu) refreshNative(st app.State) {
	cfg := st.Config
	m.status.SetTitle(headerLine(st, time.Now().Before(m.copiedUntil)))
	setEnabled(m.status, st.Sharing && st.URL != "")
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

	// Waiting requests.
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
		setChecked(item, st.Camera == nil && i == cfg.Monitor)
	}
	for i, item := range m.cameras {
		setChecked(item, st.Camera == &m.a.Cameras[i])
	}
	if m.bubbleOff != nil {
		setChecked(m.bubbleOff, st.Bubble == nil)
		setEnabled(m.bubbleOff, st.Camera == nil)
		for i, item := range m.bubbles {
			setChecked(item, st.Bubble == &m.a.Cameras[i])
			setEnabled(item, st.Camera == nil)
		}
		for i, item := range m.corners {
			setChecked(item, cfg.BubbleCorner == bubbleCorners[i].id)
		}
	}
	for i, item := range m.presets {
		setChecked(item, app.Presets[i].ID == cfg.Quality)
	}
	for i, item := range m.bitrates {
		setChecked(item, app.Bitrates[i] == cfg.EffectiveKbps())
	}

	setChecked(m.shareSound, cfg.ShareAudio)
	setChecked(m.modeAll, cfg.AudioMode != audio.ModeWhitelist)
	setChecked(m.modeList, cfg.AudioMode == audio.ModeWhitelist)

	m.appNames = m.appNames[:0]
	shown := 0
	for _, ap := range st.Apps {
		// Only whitelist mode lists apps, and voice chat is never offered.
		if shown >= len(m.appItems) || cfg.AudioMode != audio.ModeWhitelist {
			break
		}
		if ap.Blocked {
			continue
		}
		item := m.appItems[shown]
		item.SetTitle(appTitle(ap))
		setChecked(item, appChecked(cfg, ap))
		setEnabled(item, appToggleable(cfg, ap))
		item.Show()
		m.appNames = append(m.appNames, ap.Name)
		shown++
	}
	for i := shown; i < len(m.appItems); i++ {
		m.appItems[i].Hide()
	}
	setVisible(m.noApps, shown == 0 && cfg.AudioMode == audio.ModeWhitelist)

	setChecked(m.netInternet, st.Local == nil)
	m.addrs = localChoices(st)
	for i, item := range m.addrItems {
		if i < len(m.addrs) {
			item.SetTitle(m.addrs[i].String())
			setChecked(item, m.addrs[i].IP.Equal(st.Local))
			item.Show()
		} else {
			item.Hide()
		}
	}
	setChecked(m.approval, cfg.RequireApproval)
	setChecked(m.hideIP, cfg.RelayOnly())
	setEnabled(m.hideIP, st.Local == nil && cfg.TURN.Configured())
	setEnabled(m.relay, st.Local == nil)

	setChecked(m.askAlone, cfg.AskWhenAlone)
	setChecked(m.autostart, platform.AutostartEnabled())
}

func appTitle(ap audio.AppStatus) string {
	switch {
	case ap.Blocked:
		return ap.Display + " — voice chat, never shared"
	case !ap.Running:
		return ap.Display + " (not playing)"
	}
	return ap.Display
}

// appChecked shows whether friends hear the app (or, in whitelist mode,
// whether it's whitelisted).
func appChecked(cfg app.Config, ap audio.AppStatus) bool {
	if cfg.AudioMode == audio.ModeWhitelist {
		return !ap.Blocked && containsApp(cfg.Whitelist, ap.Name)
	}
	return ap.Sharing
}

// appToggleable: only whitelist mode lets you pick apps.
func appToggleable(cfg app.Config, ap audio.AppStatus) bool {
	return !ap.Blocked && cfg.AudioMode == audio.ModeWhitelist && cfg.ShareAudio
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
