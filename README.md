# Movie night 🍿

Watch movies with your friends, right in their browser.

Share one monitor and the sound of the apps you choose. Click **Start sharing**, send the invite link, and let each friend in from the popcorn icon in the taskbar tray.
Friends open the link in any browser (PC, Android, iPhone/iPad), with nothing to install, and each sets their own volume.

## Install

| | |
|---|---|
| **Windows 10/11** | Double-click `dist\MovieNight-Setup-1.1.1.exe`. It installs the app, ffmpeg, cloudflared, shortcuts and a firewall rule. |
| **Ubuntu 22.04** | Double-click `dist/movienight_1.1.1_amd64.deb` (or `sudo apt install ./movienight_1.1.1_amd64.deb`), then open *Movie night* from the app grid. |

On Ubuntu, screen capture needs the **"Ubuntu on Xorg"** session (pick it with the ⚙ on the login screen). The tray icon uses the AppIndicator extension, which Ubuntu enables by default.

## How a movie night goes

1. Popcorn menu → **Start sharing**. Within a few seconds the link (`https://random-words.trycloudflare.com/s/<secret>/`) is ready.
2. **Copy invite link** and send it.
3. A friend opens it, types a name and taps **Ask to join**. A chime plays and a popup asks *“Alice” wants to watch the movie. Let them in?*
   The icon also shows a blue dot, and the menu offers **✓ Let “Alice” in** / **✗ Deny**; whichever you use first wins.
   Requests you don't answer expire after 2 minutes, and their popup closes.
4. Allowed friends watch. A join sound plays in the browsers of those already watching (at each viewer's own volume), and the bottom-left corner of the page shows how many are watching; hover (or tap) it to see their names. **Watching** lists them; click a name to kick them.
5. When a friend leaves (closes the page, loses connection or is kicked), their connection is fully closed and they have to ask again.
   **Stop sharing** disconnects everyone and kills the link and the tunnel. The next session gets a new link.

## Tray menu

- **Status**, **Copy invite link**, **Open viewer page**
- **Start sharing** / **Stop sharing**, and **Pause picture** (freezes; friends stay connected)
- **Waiting to join**: Allow/Deny for each request. **Watching (n)**: click to kick.
- **Screen**: which monitor. **Quality**: 480p / 720p / 1080p at 24 fps, plus **Bitrate (advanced)**, 500 kbps to 8 Mbps.
- **Sound**: *All apps (except voice chat)* or *Only the apps checked below*. **Discord and TeamSpeak are never shared.**
- **Network**:
  - **Internet link** (default) or **Tailscale only**
  - **Ask me before anyone joins** (on by default)
  - **Hide my IP (relay only)**
  - **Relay server settings…**
- **Start with Windows / at login**, **How it works** (guide), **Open log**, **Quit**

Settings live in `%APPDATA%\MovieNight\config.json` (Windows) or `~/.config/movienight/config.json` (Linux).
Extra options there: `blocklist` (more apps never to share), `encoder`, `icePort` (8443), `port` (8080, Tailscale mode only), and `turn`:

```jsonc
"turn": { "cloudflareKeyId": "…", "cloudflareApiToken": "…" }                          // Cloudflare Realtime TURN
"turn": { "urls": ["turn:relay.example.com:3478"], "username": "…", "credential": "…" }  // your own coturn
```

Relay settings are read again each time you **Start sharing**.

## How it works

```
Friend's browser ── HTTPS ──► trycloudflare.com ──► cloudflared ──► Movie night web server (127.0.0.1 only)
   (viewer page, "can I join?", WebRTC handshake)

Friend's browser ◄══ WebRTC: H.264 + Opus, encrypted, peer-to-peer ══ Movie night (UDP 8443)
                     found via STUN; through a TURN relay only when a direct path is impossible

ffmpeg: screen (ddagrab / x11grab) → H.264 ─RTP→ ┐
per-app sound → mixer → ffmpeg → Opus  ─RTP→ ┴→ Go/Pion fan-out (no re-encoding)
```

- **Link and access:**
  - A Cloudflare *quick tunnel* (no account, no open router ports) gives the local web server a public HTTPS address.
  - Every path sits under a random 128-bit token that changes each share session; anything else gets a "link isn't active" page.
- **Join tickets:**
  - The viewer page long-polls for your answer.
  - An allowed ticket opens exactly one connection; it's spent after that, and revoked when the connection ends.
  - Each IP can make at most 3 requests a minute, and at most 5 can wait at once.
- **Media path:** video never goes through the tunnel. Movie night listens on UDP 8443 on every IPv4 *and IPv6* address. Each side learns its public address from public STUN servers (Cloudflare, Google), and the browser connects directly.
  - **IPv6** has no address translation, so it reaches friends whose IPv4 sits behind a strict carrier NAT, provided both sides have IPv6 (common on Brazilian ISPs and mobile networks).
  - **What's left:** friends with neither path (IPv4-only behind strict NAT, VPNs) need the optional TURN relay.
  - **The log** records the path each viewer used, and what a browser offered when it couldn't connect.
- **Windows sound:** each allowed app is recorded with WASAPI *process loopback*, and the streams are mixed.
- **Linux sound:** allowed apps are moved into a `movienight` PulseAudio sink that is looped back to your speakers.
- **Video encoder:** picked automatically: NVENC → QSV → AMF → x264. NVENC with the bundled ffmpeg 9.x needs NVIDIA driver 610+.
- **Alert sounds:** they play on your speakers only and are never shared with friends (in-process on Windows; on Linux, a PulseAudio client the mixer skips).
- **Helper processes:** ffmpeg and cloudflared die with Movie night, even if it crashes (a Windows job object; on Linux, `Pdeathsig`).
- **Upload:** you send one copy of the stream per viewer (≈ bitrate × viewers).

## Build

Needs Go, ffmpeg, Inno Setup, go-winres and nfpm (see `build.ps1`). The script downloads a pinned cloudflared release and checks its SHA256. Run:

```powershell
powershell -ExecutionPolicy Bypass -File build.ps1 -Version 1.1.1
```

Outputs land in `dist\`. Run `go run ./tools/genicon` to redraw the icons and `go test ./stream/` for the join/ticket rules.
For development, `go run . -headless` starts sharing right away without the tray. Anyone with the link gets in, and the link is printed to the log.
