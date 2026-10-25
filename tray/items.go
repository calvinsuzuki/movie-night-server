package tray

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"fyne.io/systray"

	"movienight/app"
	"movienight/audio"
	"movienight/platform"
	"movienight/ui"
)

// items is the popcorn menu as Movie night draws it itself (Windows). It
// runs on the UI thread every time the menu is redrawn.
func (m *menu) items() []ui.Item {
	st := m.a.State()
	cfg := st.Config
	m.mu.Lock()
	copied := time.Now().Before(m.copiedUntil)
	m.mu.Unlock()

	tone := ui.ToneNone
	switch {
	case st.Status != "":
		tone = ui.ToneBad
	case st.Sharing && (st.URL == "" || st.Paused):
		tone = ui.ToneWarn
	case st.Sharing:
		tone = ui.ToneLive
	}
	header := ui.Item{Kind: ui.Header, ID: "status", Label: platform.AppName, Detail: headerLine(st, copied), Tone: tone}
	if st.Sharing && st.URL != "" {
		header.OnClick, header.KeepOpen = func() { m.copy() }, true // the link copies on a click
	}
	items := []ui.Item{header}
	if st.Status != "" {
		items = append(items, ui.Item{Kind: ui.Note, ID: "problem", Label: st.Status})
	}
	if st.Sharing {
		link := ui.Item{Kind: ui.Action, ID: "copy", Icon: ui.IconLink, Label: "Copy invite link", Disabled: st.URL == "",
			OnClick: func() { m.copy() }, KeepOpen: true}
		if copied {
			link.Label, link.Checked = "Copied!", true
		}
		page := ui.Item{Kind: ui.Action, ID: "open", Icon: ui.IconEye, Label: "Open viewer page", Disabled: st.URL == "", OnClick: m.openViewerPage}
		pause := ui.Item{Kind: ui.Action, ID: "pause", Icon: ui.IconPause, Label: "Pause picture", OnClick: m.togglePause, KeepOpen: true}
		if st.Paused {
			pause.Label, pause.Checked, pause.Tone = "Resume picture", true, ui.ToneWarn
		}
		items = append(items, ui.Item{Kind: ui.Primary, ID: "share", Label: "Stop sharing", Tone: ui.ToneBad, OnClick: m.a.StopSharing,
			Children: []ui.Item{pause, link, page}})
	} else {
		items = append(items, ui.Item{Kind: ui.Primary, ID: "share", Label: "Start sharing", OnClick: m.a.StartSharing, KeepOpen: true})
	}

	if len(st.Requests) > 0 {
		items = append(items, ui.Item{Kind: ui.Label, ID: "waiting", Label: "Waiting to join"})
		for _, r := range st.Requests {
			items = append(items, ui.Item{Kind: ui.Request, ID: "request " + r.Ticket, Label: r.Name, Detail: "wants to watch",
				OnClick: func() { m.a.Allow(r.Ticket) }, OnAlt: func() { m.a.Deny(r.Ticket) }})
		}
	}
	if st.Sharing {
		items = append(items, ui.Item{Kind: ui.Label, ID: "watching", Label: fmt.Sprintf("Watching (%d)", len(st.Viewers))})
		if len(st.Viewers) == 0 {
			items = append(items, ui.Item{Kind: ui.Note, ID: "nobody", Label: "Nobody yet. Send your friends the invite link."})
		}
		for _, v := range st.Viewers {
			items = append(items, ui.Item{Kind: ui.Viewer, ID: "viewer " + v.ID, Label: v.Name, Detail: since(v.Since),
				OnClick: func() { m.a.Kick(v.ID) }, KeepOpen: true})
		}
	}

	items = append(items, ui.Item{Kind: ui.Separator, ID: "sep settings"},
		m.screenSection(st), m.bubbleSection(st), qualitySection(m.a, cfg), soundSection(m.a, st),
		ui.Item{Kind: ui.Section, ID: "advanced", Label: "Advanced", Children: []ui.Item{
			networkSection(m.a, st),
			{Kind: ui.Section, ID: "overlay", Label: "Screen overlays", Children: []ui.Item{
				{Kind: ui.Toggle, ID: "show overlay", Label: "Watchers and emoji bubbles", Checked: cfg.ShowOverlay,
					OnClick: func() { m.a.SetShowOverlay(!m.a.Config().ShowOverlay) }, KeepOpen: true},
				{Kind: ui.Toggle, ID: "ask alone", Label: "Ask to stop when alone", Checked: cfg.AskWhenAlone,
					OnClick: func() { m.a.SetAskWhenAlone(!m.a.Config().AskWhenAlone) }, KeepOpen: true},
				{Kind: ui.Note, ID: "overlay note", Label: "Friends never see these: they're left out of what you share."},
			}},
			{Kind: ui.Toggle, ID: "autostart", Label: platform.AutostartLabel(), Checked: platform.AutostartEnabled(),
				OnClick: m.toggleAutostart, KeepOpen: true},
			{Kind: ui.Action, ID: "guide", Label: "How it works", OnClick: OpenGuide},
			{Kind: ui.Action, ID: "log", Label: "Open log", OnClick: m.showLog},
		}},
		ui.Item{Kind: ui.Separator, ID: "sep app"},
		ui.Item{Kind: ui.Action, ID: "quit", Label: "Quit " + platform.AppName, OnClick: systray.Quit},
	)
	return items
}

// screenSection picks what friends see: a monitor or a camera.
func (m *menu) screenSection(st app.State) ui.Item {
	cfg := st.Config
	onMonitor := st.Camera == nil
	sec := ui.Item{Kind: ui.Section, ID: "screen", Label: "Screen", Detail: m.a.Monitors[cfg.Monitor].String()}
	var marks []ui.Mark
	for i, mon := range m.a.Monitors {
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Radio, ID: fmt.Sprintf("monitor %d", i), Label: mon.String(),
			Checked: onMonitor && i == cfg.Monitor, OnClick: func() { m.a.SetMonitor(i) }, KeepOpen: true})
		marks = append(marks, ui.Mark{Label: fmt.Sprint(i + 1), Detail: fmt.Sprintf("%d×%d", mon.Width, mon.Height),
			Selected: onMonitor && i == cfg.Monitor, Adapter: mon.Adapter, Output: mon.Output})
	}
	for i := range m.a.Cameras {
		cam := &m.a.Cameras[i]
		on := st.Camera == cam
		if on {
			sec.Detail = "Camera · " + cam.Name
		}
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Radio, ID: "camera " + cam.Name, Label: "Camera · " + cam.Name,
			Checked: on, OnClick: func() { m.a.SetCamera(i) }, KeepOpen: true})
	}
	// While it's open, each monitor shows its number.
	sec.OnOpen = func(open bool) {
		if open {
			ui.Identify(marks)
		} else {
			ui.Identify(nil)
		}
	}
	return sec
}

// bubbleSection puts a camera in a corner of the shared monitor.
func (m *menu) bubbleSection(st app.State) ui.Item {
	sec := ui.Item{Kind: ui.Section, ID: "bubble", Label: "Camera bubble", Detail: "Off"}
	if st.Camera != nil {
		sec.Detail = "—"
		sec.Children = []ui.Item{{Kind: ui.Note, ID: "bubble note", Label: "You're sharing a camera. Pick a monitor under Screen to add a camera bubble."}}
		return sec
	}
	sec.Children = []ui.Item{{Kind: ui.Radio, ID: "bubble off", Label: "Off", Checked: st.Bubble == nil,
		OnClick: func() { m.a.SetBubble(-1) }, KeepOpen: true}}
	for i := range m.a.Cameras {
		cam := &m.a.Cameras[i]
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Radio, ID: "bubble " + cam.Name, Label: cam.Name,
			Checked: st.Bubble == cam, OnClick: func() { m.a.SetBubble(i) }, KeepOpen: true})
	}
	where := ui.Item{Kind: ui.Section, ID: "bubble corner", Label: "Corner", Disabled: st.Bubble == nil}
	for _, c := range bubbleCorners {
		on := st.Config.BubbleCorner == c.id
		if on {
			where.Detail = c.label
		}
		where.Children = append(where.Children, ui.Item{Kind: ui.Radio, ID: "corner " + c.id, Label: c.label, Checked: on,
			OnClick: func() { m.a.SetBubbleCorner(c.id) }, KeepOpen: true})
	}
	if st.Bubble != nil {
		sec.Detail = st.Bubble.Name + " · " + where.Detail
	}
	sec.Children = append(sec.Children, where)
	if len(m.a.Cameras) == 0 {
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Note, ID: "no cameras", Label: "No cameras found. Plug one in and restart Movie night."})
	}
	return sec
}

func qualitySection(a *app.App, cfg app.Config) ui.Item {
	sec := ui.Item{Kind: ui.Section, ID: "quality", Label: "Quality"}
	for _, p := range app.Presets {
		if p.ID == cfg.Quality {
			sec.Detail = fmt.Sprintf("%s · %s", p.ID, kbps(cfg.EffectiveKbps()))
		}
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Radio, ID: "preset " + p.ID, Label: fmt.Sprintf("%s  (%s)", p.Label, kbps(p.Kbps)),
			Checked: p.ID == cfg.Quality, OnClick: func() { a.SetQuality(p.ID) }, KeepOpen: true})
	}
	// The preset picks its bitrate here; picking one by hand lasts until the
	// next preset is picked.
	br := ui.Item{Kind: ui.Section, ID: "bitrate", Label: "Bitrate", Detail: kbps(cfg.EffectiveKbps())}
	for _, b := range app.Bitrates {
		br.Children = append(br.Children, ui.Item{Kind: ui.Radio, ID: fmt.Sprintf("bitrate %d", b), Label: kbps(b),
			Checked: cfg.EffectiveKbps() == b, OnClick: func() { a.SetBitrate(b) }, KeepOpen: true})
	}
	sec.Children = append(sec.Children, br)
	return sec
}

func soundSection(a *app.App, st app.State) ui.Item {
	cfg := st.Config
	whitelist := cfg.AudioMode == audio.ModeWhitelist
	sec := ui.Item{Kind: ui.Section, ID: "sound", Label: "Sound", Detail: "All apps"}
	switch {
	case !cfg.ShareAudio:
		sec.Detail = "Off"
	case whitelist:
		sec.Detail = "Whitelist"
	}
	sec.Children = []ui.Item{
		{Kind: ui.Toggle, ID: "share sound", Label: "Share sound", Checked: cfg.ShareAudio,
			OnClick: func() { a.SetShareAudio(!a.Config().ShareAudio) }, KeepOpen: true},
		{Kind: ui.Radio, ID: "sound all", Label: "All apps (except voice chat)", Checked: !whitelist,
			Disabled: !cfg.ShareAudio, OnClick: func() { a.SetAudioMode(audio.ModeAll) }, KeepOpen: true},
		{Kind: ui.Radio, ID: "sound list", Label: "Whitelist apps", Checked: whitelist,
			Disabled: !cfg.ShareAudio, OnClick: func() { a.SetAudioMode(audio.ModeWhitelist) }, KeepOpen: true},
	}
	if !whitelist {
		return sec
	}
	// Voice chat is never shared, so it isn't offered.
	shown := 0
	for _, ap := range st.Apps {
		if ap.Blocked || shown == appSlots {
			continue
		}
		shown++
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Check, ID: "app " + ap.Name, Label: appTitle(ap),
			Checked: appChecked(cfg, ap), Disabled: !appToggleable(cfg, ap), OnClick: func() { a.ToggleApp(ap.Name) }, KeepOpen: true})
	}
	if shown == 0 {
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Note, ID: "no apps", Label: "No apps are playing sound."})
	}
	return sec
}

// networkSection picks how friends reach you: the internet link, or one of
// this computer's own addresses (home network, Tailscale) for this run only.
func networkSection(a *app.App, st app.State) ui.Item {
	cfg := st.Config
	internet := st.Local == nil
	sec := ui.Item{Kind: ui.Section, ID: "network", Label: "Network", Detail: "Internet link"}
	sec.Children = []ui.Item{{Kind: ui.Radio, ID: "net internet", Label: "Internet link (anyone you let in)", Checked: internet,
		OnClick: func() { a.SetLocal(nil) }, KeepOpen: true}}
	for _, ad := range localChoices(st) {
		on := ad.IP.Equal(st.Local)
		if on {
			sec.Detail = ad.Name
		}
		sec.Children = append(sec.Children, ui.Item{Kind: ui.Radio, ID: "net " + ad.IP.String(), Label: ad.String(), Checked: on,
			OnClick: func() { a.SetLocal(ad.IP) }, KeepOpen: true})
	}
	sec.Children = append(sec.Children,
		ui.Item{Kind: ui.Note, ID: "net note", Label: "An address reaches only people on that network (or your Tailscale). " +
			"It lasts until Movie night quits; next time it starts on the internet link."},
		ui.Item{Kind: ui.Toggle, ID: "approval", Label: "Ask me before anyone joins", Checked: cfg.RequireApproval,
			OnClick: func() { a.SetRequireApproval(!a.Config().RequireApproval) }, KeepOpen: true},
		ui.Item{Kind: ui.Toggle, ID: "hide ip", Label: "Hide my IP (relay only)", Checked: cfg.RelayOnly(),
			Disabled: !internet || !cfg.TURN.Configured(), OnClick: func() { a.SetHideIP(!a.Config().HideIP) }, KeepOpen: true},
		ui.Item{Kind: ui.Action, ID: "relay", Label: "Relay server settings…", Disabled: !internet,
			OnClick: func() { platform.Open(a.ConfigPath()) }},
	)
	return sec
}

// localChoices are the addresses sharing can be tied to. The one in use
// stays listed even if its network went away, so it can be switched off.
func localChoices(st app.State) []app.Address {
	addrs := app.Addresses()
	if st.Local != nil && !slices.ContainsFunc(addrs, func(ad app.Address) bool { return ad.IP.Equal(st.Local) }) {
		addrs = append(addrs, app.Address{IP: st.Local, Name: "Not connected"})
	}
	return addrs
}

// headerLine is the menu's status under the title: the link itself while
// sharing (a click copies it).
func headerLine(st app.State, copied bool) string {
	link := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(st.URL, "https://"), "http://"), "/")
	switch {
	case st.Status != "":
		return "⚠ " + st.Status
	case !st.Sharing:
		return "Not sharing"
	case st.URL == "":
		return "Creating your link…"
	case copied:
		return "Link copied ✓"
	case st.Paused:
		return "Paused · " + link
	}
	return "Sharing · " + link
}
