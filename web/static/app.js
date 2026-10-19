'use strict';

const $ = (id) => document.getElementById(id);
const stage = $('stage'), video = $('video'), overlay = $('overlay');
const vol = $('vol'), volLabel = $('volLabel'), muteBtn = $('mute'), stateEl = $('state');
const statsEl = $('stats'), nameInput = $('name');

// Everything lives under /s/<token>/, so the link stops working when the host stops sharing.
const base = location.pathname.replace(/[^/]*$/, '');

const store = {
  get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private mode */ } },
};

let volume = store.get('volume', 80);
let muted = store.get('muted', false);
let pc = null, resource = null, ticket = null, statsTimer = null, joined = false;
let watched = false; // saw the stream at least once (gets the goodbye when it ends)
let askToken = 0; // bumps on every new attempt so stale loops stop

// iOS Safari ignores media-element volume, so route audio through Web Audio there.
const elementVolumeWorks = (() => { const a = document.createElement('audio'); a.volume = 0.5; return a.volume === 0.5; })();
let actx = null, gain = null, source = null;

function applyVolume() {
  const g = muted ? 0 : Math.pow(volume / 100, 2); // perceptual curve
  if (gain) gain.gain.value = g;
  else { video.volume = Math.min(1, g); video.muted = muted || !joined; }
  vol.value = volume;
  vol.style.setProperty('--fill', `${muted ? 0 : volume}%`);
  volLabel.textContent = muted ? 'muted' : `${volume}%`;
  $('muteIcon').setAttribute('d', muted
    ? 'M16.5 12A4.5 4.5 0 0 0 14 8v2.2l2.5 2.5V12zM19 12c0 .9-.2 1.8-.5 2.6l1.5 1.5A8.9 8.9 0 0 0 21 12a9 9 0 0 0-7-8.8v2.1a7 7 0 0 1 5 6.7zM4.3 3 3 4.3 7.7 9H3v6h4l5 5v-6.7l4.3 4.3a7 7 0 0 1-2.3 1.2v2.1a9 9 0 0 0 3.7-1.9l2 2 1.3-1.3L4.3 3zM12 4 9.9 6.1 12 8.2V4z'
    : 'M3 9v6h4l5 5V4L7 9H3zm13.5 3A4.5 4.5 0 0 0 14 8v8a4.5 4.5 0 0 0 2.5-4zM14 3.2v2.1a7 7 0 0 1 0 13.4v2.1a9 9 0 0 0 0-17.6z');
}

vol.addEventListener('input', () => { volume = +vol.value; muted = false; store.set('volume', volume); store.set('muted', muted); applyVolume(); });
muteBtn.addEventListener('click', () => { muted = !muted; store.set('muted', muted); applyVolume(); });

function setState(text, cls) { stateEl.textContent = text; stateEl.className = 'pill ' + (cls || ''); }

// --- Screens -------------------------------------------------------------

function show(screen) {
  for (const id of ['askForm', 'waiting', 'notice', 'tapToPlay']) $(id).hidden = id !== screen;
  overlay.hidden = !screen;
  // The name and goodbye screens get the movie-night backdrop.
  overlay.classList.toggle('backdrop', screen === 'askForm' || (screen === 'notice' && !$('goodbye').hidden));
}

function notice(text, canAskAgain = true, goodbye = false) {
  $('noticeText').textContent = text;
  $('again').hidden = !canAskAgain;
  $('goodbye').hidden = !goodbye;
  show('notice');
  setState('offline', 'bad');
}

function waiting(text) {
  $('waitText').textContent = text;
  show('waiting');
  setState('waiting', 'warn');
}

// --- Asking the host -----------------------------------------------------

const delay = (ms) => new Promise((r) => setTimeout(r, ms));

async function ask(text = 'Waiting for the host to let you in…') {
  const mine = ++askToken;
  teardown();
  waiting(text);
  let res;
  try {
    res = await fetch(base + 'join', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: nameInput.value.trim() }),
    });
  } catch {
    return notice("Can't reach the host. Check your internet connection and try again.");
  }
  if (mine !== askToken) return;
  if (res.status === 404) return notice('The host stopped sharing. Ask them for a new link.', false);
  if (res.status === 429) return notice('Too many people are knocking right now. Try again in a minute.');
  if (!res.ok) return notice(`Something went wrong (${res.status}). Try again.`);
  ticket = (await res.json()).ticket;

  // Long-poll until the host decides.
  for (;;) {
    let state;
    try {
      const r = await fetch(`${base}join/${ticket}`, { cache: 'no-store' });
      if (r.status === 404) state = 'stopped';
      else state = (await r.json()).state;
    } catch {
      if (mine !== askToken) return;
      await delay(2000);
      continue;
    }
    if (mine !== askToken) return;
    if (state === 'pending') continue;
    if (state === 'allowed') return connect(mine);
    return ended(state);
  }
}

function ended(state) {
  ({
    denied: () => notice('The host said no this time.'),
    expired: () => notice("The host didn't answer in time."),
    kicked: () => notice('The host removed you from the stream.'),
    stopped: () => watched
      ? (video.srcObject = null, notice('The host ended the stream. Thanks for watching, see you next movie night!', false, true))
      : notice('The host stopped sharing. Ask them for a new link.', false),
  }[state] || (() => notice('You left the stream.')))();
}

async function cancel() {
  askToken++;
  if (ticket) fetch(`${base}join/${ticket}`, { method: 'DELETE', keepalive: true }).catch(() => {});
  ticket = null;
  show('askForm');
  setState('offline', 'bad');
}

// --- Watching ------------------------------------------------------------

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

function play() {
  video.play().then(() => { if (!$('tapToPlay').hidden) show(null); })
    .catch(() => show('tapToPlay')); // autoplay with sound refused: one more tap
}

async function connect(mine) {
  waiting('Connecting…');
  setState('connecting', 'warn');
  let iceServers = [];
  try { iceServers = (await (await fetch(base + 'config', { cache: 'no-store' })).json()).iceServers || []; } catch { /* STUN-less still works on a LAN */ }
  if (mine !== askToken) return;

  const conn = new RTCPeerConnection({ iceServers: iceServers.map((s) => ({ urls: s.urls, username: s.username, credential: s.credential })) });
  pc = conn;
  conn.addTransceiver('video', { direction: 'recvonly' });
  conn.addTransceiver('audio', { direction: 'recvonly' });

  const stream = new MediaStream();
  conn.ontrack = (e) => {
    if (e.track.kind === 'audio') attachAudio(e.track);
    if (e.track.kind === 'video' || elementVolumeWorks) stream.addTrack(e.track);
    if (video.srcObject !== stream) video.srcObject = stream;
  };

  let everConnected = false, dropTimer = null;
  conn.onconnectionstatechange = () => {
    if (conn !== pc) return;
    const s = conn.connectionState;
    clearTimeout(dropTimer);
    if (s === 'connected') {
      if (!everConnected) { watchTicket(conn); watchWatchers(conn); }
      everConnected = watched = true;
      setState('live', 'live');
      show(null);
      play();
    } else if (s === 'disconnected') {
      setState('reconnecting', 'warn');
      dropTimer = setTimeout(() => lost(everConnected), 8000);
    } else if (s === 'failed' || s === 'closed') {
      lost(everConnected);
    }
  };

  try {
    await conn.setLocalDescription(await conn.createOffer());
    // Non-trickle: wait (briefly) for our candidates so the offer carries them.
    await gatherDone(conn, 3000);
    const res = await fetch(base + 'whep', {
      method: 'POST', headers: { 'Content-Type': 'application/sdp', 'X-Ticket': ticket }, body: conn.localDescription.sdp,
    });
    if (res.status === 404) return ended('stopped');
    if (!res.ok) throw new Error(`the host said ${res.status}`);
    resource = res.headers.get('Location');
    await conn.setRemoteDescription({ type: 'answer', sdp: enableStereo(await res.text()) });
  } catch (err) {
    console.error(err);
    if (conn === pc) { teardown(); notice(`Couldn't start the stream (${err.message}).`); }
  }
}

function gatherDone(conn, timeout) {
  if (conn.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    const t = setTimeout(resolve, timeout);
    conn.addEventListener('icegatheringstatechange', () => {
      if (conn.iceGatheringState === 'complete') { clearTimeout(t); resolve(); }
    });
  });
}

// The connection ended: find out why before deciding what to show.
async function lost(everConnected) {
  const mine = askToken;
  teardown();
  if (!everConnected) {
    return notice("Couldn't reach the host directly. Their network (or yours) blocks direct connections; ask them to set up a relay server in Movie night.");
  }
  // Wait for the host's side to let go of the old connection too.
  const state = await ticketState('watching');
  if (mine !== askToken) return;
  if (state === 'kicked' || state === 'stopped') return ended(state);
  // A network blip: our ticket is good again for a minute, so just reconnect.
  if (state === 'allowed') return connect(mine);
  ask('Connection dropped. Asking the host to let you back in…');
}

// While watching, keep asking the host's side about our ticket, so a kick or
// "stop sharing" shows up at once instead of after the connection times out.
async function watchTicket(conn) {
  while (conn === pc) {
    let st;
    try {
      const r = await fetch(`${base}join/${ticket}?while=watching`, { cache: 'no-store' });
      st = r.status === 404 ? 'stopped' : (await r.json()).state;
    } catch { await delay(3000); continue; }
    if (conn !== pc) return;
    if (st === 'kicked' || st === 'stopped') { teardown(); return ended(st); }
    if (st !== 'watching') return; // left: the connection handler takes over
  }
}

// --- Who's watching --------------------------------------------------------

const watchersEl = $('watchers'), watchersBtn = $('watchersBtn');

function renderWatchers(list) {
  $('watcherCount').textContent = list.length;
  watchersBtn.setAttribute('aria-label', `${list.length} watching`);
  const ul = $('watcherNames');
  ul.replaceChildren(...list.map((w) => {
    const li = document.createElement('li');
    li.textContent = w.name; // names come from other people: never as HTML
    if (w.you) { const s = document.createElement('span'); s.className = 'you'; s.textContent = ' (you)'; li.append(s); }
    return li;
  }));
  watchersEl.hidden = false;
}

// Long-polls the watcher list; rings when someone new starts watching.
async function watchWatchers(conn) {
  let version = '', known = null;
  while (conn === pc) {
    let data;
    try {
      const r = await fetch(`${base}watchers?ticket=${ticket}&since=${version}`, { cache: 'no-store' });
      if (!r.ok) throw new Error(r.status);
      data = await r.json();
    } catch { if (conn !== pc) return; await delay(3000); continue; }
    if (conn !== pc) return;
    const ids = new Set(data.watchers.map((w) => w.name + '|' + w.since));
    if (known && data.watchers.some((w) => !w.you && !known.has(w.name + '|' + w.since))) joinSound();
    known = ids;
    version = data.version;
    renderWatchers(data.watchers);
  }
}

// Plays at the viewer's own volume (through Web Audio on iPhone).
let joinBuffer = null, joinAudio = null;
async function joinSound() {
  if (muted || volume === 0) return;
  const g = Math.pow(volume / 100, 2);
  try {
    if (actx) {
      if (!joinBuffer) joinBuffer = await actx.decodeAudioData(await (await fetch(base + 'join-sound.wav')).arrayBuffer());
      const src = actx.createBufferSource();
      src.buffer = joinBuffer;
      src.connect(gain);
      src.start();
    } else {
      if (!joinAudio) joinAudio = new Audio(base + 'join-sound.wav');
      joinAudio.volume = Math.min(1, g);
      joinAudio.currentTime = 0;
      await joinAudio.play();
    }
  } catch { /* a chime isn't worth an error */ }
}

// Hover shows the list on computers; tapping toggles it on phones.
watchersBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  const open = watchersEl.classList.toggle('open');
  watchersBtn.setAttribute('aria-expanded', open);
});
document.addEventListener('click', () => { watchersEl.classList.remove('open'); watchersBtn.setAttribute('aria-expanded', 'false'); });

// What the host's side says about our ticket ('left' if we can't tell),
// waiting a while for it to change if it is still `while`.
async function ticketState(while_ = '') {
  try {
    const r = await fetch(`${base}join/${ticket}` + (while_ ? `?while=${while_}` : ''), { cache: 'no-store' });
    return r.status === 404 ? 'stopped' : (await r.json()).state;
  } catch { return 'left'; }
}

function teardown() {
  watchersEl.hidden = true;
  watchersEl.classList.remove('open');
  if (resource) fetch(resource, { method: 'DELETE', keepalive: true }).catch(() => {});
  resource = null;
  if (pc) { pc.ontrack = pc.onconnectionstatechange = null; pc.close(); }
  pc = null;
}

// --- Buttons -------------------------------------------------------------

function unlockAudio() {
  // Must run inside a tap so playing with sound is allowed later (iOS/Android).
  joined = true;
  if (!elementVolumeWorks && !actx) {
    actx = new (window.AudioContext || window.webkitAudioContext)();
    gain = actx.createGain();
    gain.connect(actx.destination);
  }
  if (actx) actx.resume();
  if (!video.srcObject) video.srcObject = new MediaStream();
  video.play().catch(() => {});
  if (!actx && !joinAudio) {
    // Safari only lets an element make sound later if it was played during a tap.
    joinAudio = new Audio(base + 'join-sound.wav');
    joinAudio.muted = true;
    joinAudio.play().then(() => { joinAudio.pause(); joinAudio.muted = false; }).catch(() => { joinAudio.muted = false; });
  }
  applyVolume();
}

$('askForm').addEventListener('submit', (e) => {
  e.preventDefault();
  store.set('name', nameInput.value.trim());
  unlockAudio();
  ask();
});
$('again').addEventListener('click', () => { unlockAudio(); ask(); });
$('cancel').addEventListener('click', cancel);
$('play').addEventListener('click', () => { unlockAudio(); play(); });

window.addEventListener('pagehide', () => {
  if (ticket && !resource) fetch(`${base}join/${ticket}`, { method: 'DELETE', keepalive: true }).catch(() => {});
  teardown();
});

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
  idleTimer = setTimeout(() => { if (pc && overlay.hidden) stage.classList.add('idle'); }, 3000);
}
['mousemove', 'touchstart', 'keydown'].forEach((ev) => stage.addEventListener(ev, wake, { passive: true }));

// Keyboard: up/down volume, m mute, f fullscreen.
document.addEventListener('keydown', (e) => {
  if (e.target === vol || e.target === nameInput) return;
  if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
    volume = Math.max(0, Math.min(100, volume + (e.key === 'ArrowUp' ? 5 : -5)));
    muted = false; store.set('volume', volume); store.set('muted', muted); applyVolume(); e.preventDefault();
  } else if (e.key === 'm') muteBtn.click();
  else if (e.key === 'f') $('fs').click();
});

// Quality badge (top right, always while watching) and the stats overlay
// (the ⓘ button), both fed from one getStats() a second.
const qualityEl = $('quality');
let last = null;
$('statsBtn').addEventListener('click', () => { statsEl.hidden = !statsEl.hidden; updateStats(); });
statsTimer = setInterval(updateStats, 1000);

function resolutionName(w, h) {
  if (!h) return '–';
  const p = Math.min(w * 9 / 16, h); // a 21:9 movie in 1080p is still "1080p"
  return p >= 1000 ? '1080p' : p >= 680 ? '720p' : p >= 460 ? '480p' : `${h}p`;
}

function renderQuality(v, kbps, rttMs) {
  qualityEl.hidden = false;
  $('qRes').textContent = resolutionName(v.frameWidth, v.frameHeight);
  $('qFps').textContent = `${Math.round(v.framesPerSecond || 0)} fps`;
  $('qRate').textContent = kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${Math.round(kbps)} kbps`;
  $('qPing').textContent = rttMs == null ? '– ms' : rttMs < 1 ? '<1 ms' : `${rttMs} ms`;
  qualityEl.className = rttMs == null ? '' : rttMs < 100 ? 'good' : rttMs < 200 ? 'ok' : 'bad';
}

async function updateStats() {
  if (!pc || pc.connectionState !== 'connected') {
    qualityEl.hidden = true;
    last = null;
    statsEl.textContent = pc ? 'connecting…' : 'not connected';
    return;
  }
  const report = await pc.getStats();
  let v = null, a = null, pair = null;
  report.forEach((s) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') v = s;
    if (s.type === 'inbound-rtp' && s.kind === 'audio') a = s;
    if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s;
  });
  if (!v) { qualityEl.hidden = true; statsEl.textContent = 'waiting for video…'; return; }
  const local = pair && report.get(pair.localCandidateId);
  const now = performance.now();
  let kbps = 0;
  if (last) kbps = ((v.bytesReceived + (a ? a.bytesReceived : 0) - last.bytes) * 8) / (now - last.t);
  last = { t: now, bytes: v.bytesReceived + (a ? a.bytesReceived : 0) };
  const rttMs = pair && pair.currentRoundTripTime != null ? Math.round(pair.currentRoundTripTime * 1000) : null;
  renderQuality(v, kbps, rttMs);
  if (statsEl.hidden) return;
  const jb = (s) => (s && s.jitterBufferEmittedCount ? Math.round((s.jitterBufferDelay / s.jitterBufferEmittedCount) * 1000) : 0);
  statsEl.textContent = [
    `video   ${v.frameWidth || '?'}x${v.frameHeight || '?'} @ ${Math.round(v.framesPerSecond || 0)} fps`,
    `bitrate ${(kbps / 1000).toFixed(2)} Mbps`,
    `buffer  video ${jb(v)} ms · audio ${jb(a)} ms`,
    `rtt     ${rttMs != null ? rttMs + ' ms' : '?'}`,
    `path    ${local ? (local.candidateType === 'relay' ? 'relay server' : 'direct') + ` (${local.candidateType})` : '?'}`,
    `lost    ${v.packetsLost || 0} pkts · nacks ${v.nackCount || 0} · freezes ${v.freezeCount || 0}`,
    `dropped ${v.framesDropped || 0} frames`,
  ].join('\n');
}

nameInput.value = store.get('name', '');
show('askForm');
setState('offline', 'bad');
applyVolume();
