'use strict';

const $ = (id) => document.getElementById(id);
const stage = $('stage'), video = $('video'), overlay = $('overlay'), msg = $('msg');
const vol = $('vol'), volLabel = $('volLabel'), muteBtn = $('mute'), stateEl = $('state');
const statsEl = $('stats');

const store = {
  get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private mode */ } },
};

let volume = store.get('volume', 80);
let muted = store.get('muted', false);
let pc = null, resource = null, retryTimer = null, statsTimer = null, joined = false;

// iOS Safari ignores media-element volume, so route audio through Web Audio there.
const elementVolumeWorks = (() => { const a = document.createElement('audio'); a.volume = 0.5; return a.volume === 0.5; })();
let actx = null, gain = null, source = null;

function applyVolume() {
  const g = muted ? 0 : Math.pow(volume / 100, 2); // perceptual curve
  if (gain) gain.gain.value = g;
  else { video.volume = Math.min(1, g); video.muted = muted || !joined; }
  vol.value = volume;
  volLabel.textContent = muted ? 'muted' : `${volume}%`;
  $('muteIcon').setAttribute('d', muted
    ? 'M16.5 12A4.5 4.5 0 0 0 14 8v2.2l2.5 2.5V12zM19 12c0 .9-.2 1.8-.5 2.6l1.5 1.5A8.9 8.9 0 0 0 21 12a9 9 0 0 0-7-8.8v2.1a7 7 0 0 1 5 6.7zM4.3 3 3 4.3 7.7 9H3v6h4l5 5v-6.7l4.3 4.3a7 7 0 0 1-2.3 1.2v2.1a9 9 0 0 0 3.7-1.9l2 2 1.3-1.3L4.3 3zM12 4 9.9 6.1 12 8.2V4z'
    : 'M3 9v6h4l5 5V4L7 9H3zm13.5 3A4.5 4.5 0 0 0 14 8v8a4.5 4.5 0 0 0 2.5-4zM14 3.2v2.1a7 7 0 0 1 0 13.4v2.1a9 9 0 0 0 0-17.6z');
}

vol.addEventListener('input', () => { volume = +vol.value; muted = false; store.set('volume', volume); store.set('muted', muted); applyVolume(); });
muteBtn.addEventListener('click', () => { muted = !muted; store.set('muted', muted); applyVolume(); });

function setState(text, cls) { stateEl.textContent = text; stateEl.className = 'pill ' + (cls || ''); }

function attachAudio(track) {
  if (elementVolumeWorks) return; // the <video> element plays the audio itself
  if (source) source.disconnect();
  source = actx.createMediaStreamSource(new MediaStream([track]));
  source.connect(gain);
}

// Opus is decoded as mono unless the answer advertises stereo.
function enableStereo(sdp) {
  const m = sdp.match(/a=rtpmap:(\d+) opus\/48000/i);
  if (!m) return sdp;
  return sdp.replace(new RegExp(`(a=fmtp:${m[1]} [^\\r\\n]*)`), (line) =>
    line.includes('stereo=1') ? line : `${line};stereo=1;sprop-stereo=1`);
}

async function connect() {
  clearTimeout(retryTimer);
  teardown();
  setState('connecting', 'warn');
  const conn = new RTCPeerConnection();
  pc = conn;
  conn.addTransceiver('video', { direction: 'recvonly' });
  conn.addTransceiver('audio', { direction: 'recvonly' });

  const stream = new MediaStream();
  conn.ontrack = (e) => {
    if (e.track.kind === 'audio') attachAudio(e.track);
    if (e.track.kind === 'video' || elementVolumeWorks) stream.addTrack(e.track);
    if (video.srcObject !== stream) video.srcObject = stream;
    video.play().catch(() => {});
  };

  let dropTimer = null;
  conn.onconnectionstatechange = () => {
    if (conn !== pc) return;
    const s = conn.connectionState;
    clearTimeout(dropTimer);
    if (s === 'connected') { setState('live', 'live'); msg.textContent = ''; }
    else if (s === 'disconnected') { setState('reconnecting', 'warn'); dropTimer = setTimeout(() => retry(), 3000); }
    else if (s === 'failed') retry();
  };

  try {
    await conn.setLocalDescription(await conn.createOffer());
    const res = await fetch('/whep', { method: 'POST', headers: { 'Content-Type': 'application/sdp' }, body: conn.localDescription.sdp });
    if (!res.ok) throw new Error(`server said ${res.status}: ${await res.text()}`);
    resource = res.headers.get('Location');
    await conn.setRemoteDescription({ type: 'answer', sdp: enableStereo(await res.text()) });
  } catch (err) {
    console.error(err);
    msg.textContent = `Can't reach the stream (${err.message}). Retrying…`;
    retry();
  }
}

function retry() {
  setState('offline', 'bad');
  clearTimeout(retryTimer);
  retryTimer = setTimeout(connect, 2000);
}

function teardown() {
  if (resource) fetch(resource, { method: 'DELETE', keepalive: true }).catch(() => {});
  resource = null;
  if (pc) { pc.ontrack = pc.onconnectionstatechange = null; pc.close(); }
  pc = null;
}

$('join').addEventListener('click', async () => {
  // Must run inside the tap so autoplay with sound is allowed (iOS/Android).
  joined = true;
  if (!elementVolumeWorks && !actx) {
    actx = new (window.AudioContext || window.webkitAudioContext)();
    gain = actx.createGain();
    gain.connect(actx.destination);
  }
  if (actx) await actx.resume();
  overlay.hidden = true;
  applyVolume();
  connect();
});

window.addEventListener('pagehide', teardown);

// Fullscreen: the whole stage where supported; iPhone only allows native video fullscreen.
$('fs').addEventListener('click', () => {
  if (document.fullscreenElement || document.webkitFullscreenElement) {
    (document.exitFullscreen || document.webkitExitFullscreen).call(document);
  } else if (stage.requestFullscreen) stage.requestFullscreen().catch(() => {});
  else if (stage.webkitRequestFullscreen) stage.webkitRequestFullscreen();
  else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen();
});

// Auto-hide controls while watching.
let idleTimer = null;
function wake() {
  stage.classList.remove('idle');
  clearTimeout(idleTimer);
  idleTimer = setTimeout(() => { if (joined) stage.classList.add('idle'); }, 3000);
}
['mousemove', 'touchstart', 'keydown'].forEach((ev) => stage.addEventListener(ev, wake, { passive: true }));

// Keyboard: up/down volume, m mute, f fullscreen.
document.addEventListener('keydown', (e) => {
  if (e.target === vol) return;
  if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
    volume = Math.max(0, Math.min(100, volume + (e.key === 'ArrowUp' ? 5 : -5)));
    muted = false; store.set('volume', volume); store.set('muted', muted); applyVolume(); e.preventDefault();
  } else if (e.key === 'm') muteBtn.click();
  else if (e.key === 'f') $('fs').click();
});

// Stats overlay.
let last = null;
$('statsBtn').addEventListener('click', () => {
  statsEl.hidden = !statsEl.hidden;
  clearInterval(statsTimer);
  if (!statsEl.hidden) { last = null; statsTimer = setInterval(updateStats, 1000); updateStats(); }
});

async function updateStats() {
  if (!pc) { statsEl.textContent = 'not connected'; return; }
  const report = await pc.getStats();
  let v = null, a = null, pair = null;
  report.forEach((s) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') v = s;
    if (s.type === 'inbound-rtp' && s.kind === 'audio') a = s;
    if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s;
  });
  if (!v) { statsEl.textContent = 'waiting for video…'; return; }
  const now = performance.now();
  let kbps = 0;
  if (last) kbps = ((v.bytesReceived + (a ? a.bytesReceived : 0) - last.bytes) * 8) / (now - last.t);
  last = { t: now, bytes: v.bytesReceived + (a ? a.bytesReceived : 0) };
  const jb = (s) => (s && s.jitterBufferEmittedCount ? Math.round((s.jitterBufferDelay / s.jitterBufferEmittedCount) * 1000) : 0);
  statsEl.textContent = [
    `video   ${v.frameWidth || '?'}x${v.frameHeight || '?'} @ ${Math.round(v.framesPerSecond || 0)} fps`,
    `bitrate ${(kbps / 1000).toFixed(2)} Mbps`,
    `buffer  video ${jb(v)} ms · audio ${jb(a)} ms`,
    `rtt     ${pair && pair.currentRoundTripTime != null ? Math.round(pair.currentRoundTripTime * 1000) + ' ms' : '?'}`,
    `lost    ${v.packetsLost || 0} pkts · nacks ${v.nackCount || 0} · freezes ${v.freezeCount || 0}`,
    `dropped ${v.framesDropped || 0} frames`,
  ].join('\n');
}

applyVolume();
