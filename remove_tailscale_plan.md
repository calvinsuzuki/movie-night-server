# Movie night without Tailscale: plan

## Goal
Friends can watch without installing anything.

1. The sharer clicks **Share**. Movie night gets a public HTTPS link and the sharer sends it to a friend.
2. The friend opens the link in any browser, types a name and sees "Waiting for the host…".
3. The sharer's popcorn menu shows **"Alice wants to watch — Allow / Deny"**.
4. On **Allow**, video and sound go peer-to-peer to the friend's browser, encrypted end to end with DTLS-SRTP (WebRTC's standard encryption), the same as today.
5. When the friend leaves (tab closed, connection lost, or kicked), their connection is fully torn down and their pass is revoked. Coming back means asking again.
6. **Stop sharing** (or Quit) shuts the tunnel, so the link stops working.

The current Tailscale behaviour stays as an optional **private network mode**.

## Decisions
| Topic | Choice |
|---|---|
| Public link | Cloudflare **quick tunnel**: a bundled `cloudflared` gives a random `https://<words>.trycloudflare.com` address. No account, no open ports, a new address each share. |
| Finding a direct path | Public STUN servers (Cloudflare `stun.cloudflare.com:3478` and Google `stun.l.google.com:19302`) let both sides learn their public addresses so they can connect directly. |
| When no direct path exists | Optional **TURN** relay (Cloudflare Realtime TURN, or any coturn server) set up in config. Without one, about 10–20% of friends (CGNAT, mobile data, strict firewalls) can't connect, and the page tells them so. |
| Invites | **One link per share session.** Every join request needs an Allow. A pass is valid for one connection only. |
| Tailscale | Kept as a mode: `Network: Internet link (default) / Tailscale only`. |

## How it works
```
Friend's browser ── HTTPS ──► trycloudflare.com ──► cloudflared (on sharer PC) ──► Movie night HTTP 127.0.0.1:8080
   (page, join request, WebRTC handshake only; tiny traffic)

Friend's browser ◄══ WebRTC media (UDP, direct P2P) ══ Movie night ICE port 8443
        ↑ found via STUN; through a TURN relay only if a direct path fails
```
- **Pages and handshake:** the HTTP server binds to **127.0.0.1 only**, and only cloudflared can reach it. Nothing is exposed on the LAN.
- **Video and sound:** these never pass through Cloudflare's tunnel. Only the page, the join request and the WebRTC handshake (SDP) do.

## Join and approval protocol
1. **The link carries a share token:** `https://x.trycloudflare.com/s/<shareToken>/`. The 128-bit token is random and new for each share session. Any other path returns 404, so bots scanning trycloudflare addresses hit nothing.
2. **The friend asks to join:** `POST /s/<shareToken>/join {name}` returns `{ticket}`.
   - The ticket is random and 128-bit. The name is trimmed to 32 characters.
   - At most 5 requests can wait at once, and each IP can make at most 3 requests per minute. Cloudflare passes the real IP in `CF-Connecting-IP`.
   - Requests expire after 2 minutes, which counts as Deny.
3. **The page waits:** `GET /s/<shareToken>/join/<ticket>` long-polls for up to 25 s and loops, staying under Cloudflare's 100 s request timeout. It returns `pending | allowed | denied | expired`.
   - I'm using long-polling rather than Server-Sent Events: quick tunnels are reported to not support Server-Sent Events, and long-polling works everywhere.
4. **On `allowed`:** the page sends `POST /s/<shareToken>/whep` with the header `X-Ticket: <ticket>` and the SDP offer, and gets back the SDP answer plus the `iceServers` it should use.
   - The ticket is used up when the answer is sent. If the ticket is missing, used or wrong, the response is 403.
5. **Leaving:**
   - **The friend leaves:** the page sends `DELETE /whep/<id>` (plus `pagehide` → `fetch(..., {keepalive:true})`). Otherwise, ICE `disconnected` for 10 s or `failed` triggers the close.
   - **Close:** the app closes the PeerConnection, forgets the peer and its ticket, and refreshes the tray.
6. **Kick:** the menu item closes that viewer's PeerConnection. The page shows "The host removed you" and does **not** auto-reconnect; it must ask again.
7. **Viewer page reconnect:** today it reconnects silently. It will now return to "Ask to join again" with the name pre-filled from `localStorage`. On a short network blip it asks again automatically, which still needs a fresh Allow from the sharer.

## Code changes

### stream/hub.go
- **Address filter:** `NewHub(opts)` takes a `Network` mode. In Internet mode there's no Tailscale `SetIPFilter`, but loopback, link-local and Docker/WSL virtual adapters are still filtered out.
- **ICE port:** swap `NewICEUDPMux` for `NewICEUniversalUDPMux` so the single port 8443 can also produce STUN (public-address) candidates. Set `SetNAT1To1IPs` only if needed (no).
- **ICE servers:** `webrtc.Configuration.ICEServers` = STUN plus the TURN entry if configured. Add `ICETransportPolicy: Relay` when "Hide my IP" is on.
- **Peers:** each peer stores `{id, name, remoteIP, joinedAt}`, and `Viewers()` returns that list, not a count.
- **New methods:** `Kick(id)`; `Answer` takes a name.
- **Teardown:** the `disconnected` state gets a 10 s grace timer before closing. Today only `failed`/`closed` close a peer.

### stream/lobby.go (new)
- **Contents:** the pending-request queue, tickets, rate limits and expiry, plus `Requests()`, `Allow(ticket)` and `Deny(ticket)`.
- **Changes:** an `OnChange` callback refreshes the tray, and the first request also triggers a notification.

### stream/whep.go
- **Routes:** move under `/s/{token}/`, check the ticket and add the join and poll endpoints.
- **ICE servers:** the page gets its `iceServers` from a new `GET /s/{token}/config`, which returns STUN plus short-lived TURN credentials.

### tunnel/cloudflared.go (new)
- **Starting:** `Start(ctx, localURL)` runs `cloudflared tunnel --no-autoupdate --url http://127.0.0.1:8080` through `platform.Command` (so no console window opens). It parses stderr for `https://*.trycloudflare.com` and returns the URL within a 20 s timeout.
- **Monitoring and stopping:** it restarts if the process dies and reports a new URL, which tells the sharer the link changed. `Stop()` kills the process.
- **Finding the binary:** next to the exe, then PATH.

### turn/ (new, small)
- **Static credentials (coturn):** config `TURN: {URL, Username, Credential}`.
- **Cloudflare Realtime TURN:** config `{KeyID, APIToken}`. The app mints 4-hour credentials through Cloudflare's TURN key API when sharing starts and refreshes them hourly. Check the exact endpoint against Cloudflare's docs during implementation.
- **No TURN configured:** STUN only. The viewer page shows a clear "Couldn't reach the host directly. Ask them to turn on a relay" message when ICE fails.

### app/app.go and app/config.go
- **Config:**
  - New fields: `Network` ("internet" | "tailscale"), `TURN`, `HideIP` (relay-only), `RequireApproval` (default true, also used in Tailscale mode).
  - `AllowLAN` is removed.
- **Start and Stop:**
  - `StartSharing()` in Internet mode starts the HTTP server on 127.0.0.1, mints a new share token, starts the tunnel and sets `URL = tunnelURL + "/s/" + token + "/"`.
  - `StopSharing()` stops the tunnel, kicks every viewer, clears pending requests and blanks the URL.
- **Pause:** pausing currently keeps viewers connected and just stops ffmpeg. In Internet mode, keep **Pause** (viewers stay, picture freezes) separate from **Stop sharing** (everything is torn down).
- **Tailscale mode:** the current code path, plus the approval lobby.
- **State:** `State()` gains `Requests []Request` and `Viewers []Viewer`.

### tray/tray.go
- **Top of the menu:** `● Sharing · 2 watching`, then the link, "Copy invite link", and **Stop sharing** / **Share**.
- **New "Requests" section:** reserved slots, because the systray library's menus are fixed. Each request has two items: `Alice wants to watch — Allow` and `Deny`. A notification balloon appears on Windows (`Shell_NotifyIcon` NIIF_INFO) and on Ubuntu (`notify-send`). The tray icon gets a badge variant while requests wait.
- **New "Watching" submenu:** `Alice · 12 min — Kick` for each viewer.
- **Settings:** a **Network** submenu (Internet link / Tailscale only), "Hide my IP (relay only)" (disabled until TURN is configured), and "Relay server…", which opens config.json for now.
- **Guide item:** "Tailscale guide" becomes "How it works", which opens the rewritten guide.

### web/static (index.html, app.js, style.css)
- **Screens:**
  1. Name prompt with an "Ask to join" button.
  2. "Waiting for the host…" with a Cancel button that deletes the request.
  3. Denied, Expired, Removed or Host stopped, each with an "Ask again" button.
  4. The player.
- **Requests:** fetch `config` for `iceServers`, use relative paths under `/s/{token}/`, and remember the name in `localStorage`.
- **Behaviour:** auto-reconnect is removed, replaced by the "ask again" flow above.

### main.go
- **First run:** drop the "guide on first run if Tailscale is missing" step. The guide opens only from the menu or the installer.

### Packaging
- **Windows:** bundle `cloudflared.exe` (Apache-2.0, about 35 MB, from Cloudflare's GitHub releases with the version pinned) next to ffmpeg.
  - The installer's Tailscale page becomes an optional "Private network mode (advanced)" note, unticked by default, or is removed.
  - The firewall rule stays: inbound UDP 8443 for direct P2P.
  - Finish page checkbox: "Show me how Movie night works".
- **Ubuntu .deb:** `cloudflared` isn't in Ubuntu's repos, so ship the binary at `/usr/lib/movienight/cloudflared`.
- **build.ps1:** download and verify the pinned cloudflared releases for both OSes into `dist\` (SHA256 checked).
- **README and guide:** rewrite around the link flow, with a short section for the Tailscale mode and for setting up TURN.

## Security and privacy notes
- **Who gets in:** only someone with the link *and* your Allow. The share token keeps scanners out, and rate limits and the cap of 5 waiting requests stop spam.
- **IP addresses:** a direct P2P connection reveals each side's public IP to the other. "Hide my IP" (relay-only) prevents this but needs TURN and costs relay bandwidth.
- **What Cloudflare sees:** the page, the names, the join requests and the SDP handshake, but not the media. The media is encrypted end to end with DTLS-SRTP, even when it passes through a TURN relay.
- **Local exposure:** the HTTP server never listens on anything but 127.0.0.1 in Internet mode.
- **Quick tunnels:** free with no uptime guarantee. If they become unreliable, the same protocol can move to a tiny relay server we host (Cloudflare Worker) with a fixed link.

## Phases
1. **Hub and lobby:** STUN, the universal ICE port, approval tickets, Kick, and the viewer page's join/waiting screens. Test on the LAN through `http://127.0.0.1`, plus a phone on mobile data via step 2.
2. **Tunnel:** the cloudflared manager, share token, Start/Stop lifecycle and tray link.
3. **Tray:** Requests and Watching sections, notifications, Network submenu.
4. **TURN:** static coturn config first, then Cloudflare credential minting and Hide my IP.
5. **Packaging and docs:** bundle cloudflared, update the installer, .deb, build.ps1, guide and README.

## Verification
1. **Home Wi-Fi:** share and open the link from a phone on the same Wi-Fi. A request appears, Allow plays within ~2 s, and `chrome://webrtc-internals` shows a `srflx`/`host` candidate pair.
2. **Mobile data:** the same from a phone on mobile data (a different network). Check whether it connects directly or needs TURN, and record which candidate type was used.
3. **Deny and expiry:** Deny shows "Denied". Waiting 2 minutes without answering shows "Expired".
4. **Leaving:**
   - Closing the tab drops the tray count within ~1 s.
   - Killing the phone's network drops it within ~10 s.
   - Reloading the page needs a new Allow.
5. **Kick:** the viewer sees "Removed" and doesn't come back by itself.
6. **Stop sharing:** viewers are dropped, the old link returns an error page, and the cloudflared process is gone (Task Manager).
7. **Bad requests:** a wrong token returns 404, `/whep` without a ticket returns 403, and a reused ticket returns 403. The sixth simultaneous request is refused.
8. **TURN:** set up coturn, turn on Hide my IP, and confirm only `relay` candidates are used.
9. **Tailscale mode:** it still works as before, with approval on top.
10. **Picture quality:** latency ≤1 s and 24 fps still hold on a direct connection (stopwatch test from the original plan).
