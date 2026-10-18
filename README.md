# Movie night 🍿

Share one monitor and the sound of the apps you choose with friends on your Tailscale network.
Friends open a link in any browser (PC, Android, iPhone/iPad) and watch, each with their own volume control.
The sharer controls everything from the popcorn icon in the taskbar tray.

## Install

| | |
|---|---|
| **Windows 10/11** | Double-click `dist\MovieNight-Setup-1.0.0.exe`. It installs the app, ffmpeg, shortcuts and a firewall rule. |
| **Ubuntu 22.04** | Double-click `dist/movienight_1.0.0_amd64.deb` (or `sudo apt install ./movienight_1.0.0_amd64.deb`), then open *Movie night* from the app grid. |

On Ubuntu, screen capture needs the **"Ubuntu on Xorg"** session (pick it with the ⚙ on the login screen). The tray icon uses the AppIndicator extension, which Ubuntu enables by default.

## Tray menu

- **Status**: sharing or paused, how many friends are watching, plus any problem (e.g. "Waiting for Tailscale to connect").
- **Copy viewer link** / **Open viewer page**
- **Pause / Start sharing**
- **Screen**: which monitor to share
- **Quality**: 480p / 720p / 1080p at 24 fps. **Bitrate (advanced)**: 500 kbps to 8 Mbps, or Automatic (preset default).
- **Sound**: share sound on or off; *All apps (except voice chat)* or *Only the apps checked below* (whitelist).
  **Discord and TeamSpeak are never shared**, so friends don't hear their own voices echoed back.
- **Start with Windows / Start at login**, **Open log**, **Quit**

Settings are saved in `%APPDATA%\MovieNight\config.json` (Windows) or `~/.config/movienight/config.json` (Linux).
Extra options there: `blocklist` (more apps to never share), `encoder`, `port` (8080), `icePort` (8443),
and `allowLan` (also serve viewers outside Tailscale; off by default).

## How it works

```
ffmpeg: screen (ddagrab / x11grab) → H.264 ─RTP→ ┐
per-app sound → mixer → ffmpeg → Opus  ─RTP→ ┤→ Go/Pion WebRTC fan-out → browsers over Tailscale
```

- **Windows**: each allowed app is recorded with WASAPI *process loopback* (only that app's sound), and the streams are mixed.
- **Linux**: allowed apps are moved into a `movienight` PulseAudio sink that is looped back to your speakers, so you still hear everything. ffmpeg records that sink.
- The video encoder is picked automatically: NVENC → QSV → AMF → x264. NVENC with the bundled ffmpeg 9.x needs NVIDIA driver 610+.
- Viewers are only served on your Tailscale address. If Tailscale isn't connected, Movie night waits for it.
- The sharer uploads one copy of the stream per viewer (≈ bitrate × viewers). `tailscale ping <friend>` should show a direct connection, not DERP.

## Build

Needs Go, ffmpeg, Inno Setup, go-winres and nfpm (see `build.ps1`), then run:

```powershell
powershell -ExecutionPolicy Bypass -File build.ps1 -Version 1.0.0
```

Outputs land in `dist\`. Run `go run ./tools/genicon` to redraw the icon.
For development: `go run . -headless` runs without the tray and logs to the console.
