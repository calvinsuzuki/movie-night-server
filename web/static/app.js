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
let gather = { ms: 0, done: false }; // how finding our addresses went, for reports

// iOS Safari ignores media-element volume, so route audio through Web Audio there.
const elementVolumeWorks = (() => { const a = document.createElement('audio'); a.volume = 0.5; return a.volume === 0.5; })();
let actx = null, gain = null, source = null;
// Sound effects (join chime, throws) mix through their own gain at the viewer's volume.
let fx = null, fxGain = null;
const fxBuffers = new Map(); // file → Promise<AudioBuffer>
let emojis = ['🍿']; // what can be thrown, from the host

function applyVolume() {
  const g = muted ? 0 : Math.pow(volume / 100, 2); // perceptual curve
  if (fxGain) fxGain.gain.value = g;
  if (gain) gain.gain.value = g;
  // Through the link the sound plays from the element itself (iOS still
  // ignores its volume there, but mute works).
  if (!gain || (pc && pc.cast)) { video.volume = Math.min(1, g); video.muted = muted || !joined; }
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
      body: JSON.stringify({ name: nameInput.value.trim(), host: hostKey }),
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
  try {
    const cfg = await (await fetch(base + 'config', { cache: 'no-store' })).json();
    iceServers = cfg.iceServers || [];
    if (cfg.emojis && cfg.emojis.length) emojis = cfg.emojis;
    (cfg.sounds || []).forEach(loadEffect); // so the first throw isn't silent
  } catch { /* STUN-less still works on a LAN */ }
  if (mine !== askToken) return;
  if (viaLink) return watchViaLink(mine); // no direct path last time either

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
      if (!everConnected) { watchTicket(conn); watchWatchers(conn); watchThrows(conn); }
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
    const t0 = performance.now();
    await conn.setLocalDescription(await conn.createOffer());
    // Non-trickle: wait (briefly) for our candidates so the offer carries them.
    await gatherDone(conn, 3000);
    gather = { ms: Math.round(performance.now() - t0), done: conn.iceGatheringState === 'complete' };
    const res = await fetch(base + 'whep', {
      method: 'POST', headers: { 'Content-Type': 'application/sdp', 'X-Ticket': ticket }, body: conn.localDescription.sdp,
    });
    if (res.status === 404) return ended('stopped');
    if (!res.ok) throw new Error(`the host said ${res.status}`);
    resource = res.headers.get('Location');
    await conn.setRemoteDescription({ type: 'answer', sdp: enableStereo(await res.text()) });
    // A direct path connects within a second or two; browsers take ~15 s to
    // give up on a hopeless one, so don't wait for that.
    setTimeout(() => { if (conn === pc && !everConnected) lost(false); }, 8000);
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
  const mine = askToken, conn = pc;
  if (!conn || conn.lost) return; // "failed" can follow "disconnected" while we report
  conn.lost = true;
  if (!conn.cast) await report(conn, everConnected ? 'dropped' : 'never connected');
  if (mine !== askToken) return;
  if (!everConnected && !conn.cast) {
    // No direct path between our networks: watch through the link instead.
    // Let go of the direct attempt first, so the host frees our ticket.
    const r = resource;
    resource = null;
    if (r) await fetch(r, { method: 'DELETE' }).catch(() => {});
    if (mine !== askToken) return;
    teardown();
    if (!castSupported()) {
      return notice("Couldn't reach the host directly, and this browser can't play the stream another way. Try a newer browser, or another network.");
    }
    viaLink = true;
    return watchViaLink(mine);
  }
  teardown();
  if (!everConnected) return notice("Couldn't get the stream through the link either. Try again in a moment.");
  // Wait for the host's side to let go of the old connection too.
  const state = await ticketState('watching');
  if (mine !== askToken) return;
  if (state === 'kicked' || state === 'stopped') return ended(state);
  // A network blip: our ticket is good again for a minute, so just reconnect.
  if (state === 'allowed') return connect(mine);
  ask('Connection dropped. Asking the host to let you back in…');
}

// Sends the host what this browser saw of a failed connection (its own
// addresses and every path it tried), for the host's log: friends on phones
// can't easily send their browser's logs.
async function report(conn, reason) {
  const d = {
    ticket, reason, browser: navigator.userAgent, gatherMs: gather.ms, gathered: gather.done,
    state: `ice ${conn.iceConnectionState}, connection ${conn.connectionState}`,
    network: navigator.connection ? `${navigator.connection.type || 'type unknown'}, speed like ${navigator.connection.effectiveType || '?'}` : 'unknown',
    local: [], pairs: [],
  };
  const describe = (c) => !c ? '?' : `${c.candidateType} ${c.protocol} ${c.address || c.ip || 'hidden'}:${c.port}` + (c.relayProtocol ? ` via ${c.relayProtocol}` : '');
  try {
    const stats = await conn.getStats(), cands = new Map();
    stats.forEach((s) => { if (s.type === 'local-candidate' || s.type === 'remote-candidate') cands.set(s.id, s); });
    stats.forEach((s) => {
      if (s.type === 'local-candidate') d.local.push(describe(s));
      if (s.type === 'candidate-pair') {
        d.pairs.push(`me ${describe(cands.get(s.localCandidateId))} → host ${describe(cands.get(s.remoteCandidateId))}: ${s.state}, `
          + `sent ${s.requestsSent || 0} checks, ${s.responsesReceived || 0} answered, got ${s.requestsReceived || 0} from the host`);
      }
    });
  } catch { /* the report still says what it can */ }
  fetch(base + 'report', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(d), keepalive: true,
  }).catch(() => {});
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

// --- Watching through the link -------------------------------------------

// When no direct path exists (strict NAT on both ends, as on most mobile
// data), the host sends the stream through the link itself: MP4 pieces over
// a WebSocket, played with Media Source Extensions. A bit more delay, but it
// gets through wherever the page loads.
const MS = window.ManagedMediaSource || window.MediaSource;
let viaLink = false; // fell back once: reconnects go straight to the link

function castSupported() {
  return !!(MS && window.WebSocket && MS.isTypeSupported('video/mp4; codecs="avc1.42e01f"'));
}

function watchViaLink(mine, tries = 0) {
  if (mine !== askToken) return;
  waiting('No direct path to the host. Getting the stream through the link…');
  setState('connecting', 'warn');
  const url = new URL(`${base}cast?ticket=${encodeURIComponent(ticket)}`, location.href);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(url);
  ws.binaryType = 'arraybuffer';
  const ms = new MS();
  const src = URL.createObjectURL(ms);
  const queue = []; // messages that arrived before the MediaSource opened
  let open = false, audio = false, audioInit = null, vsb = null, asb = null, feedVideo = null, feedAudio = null, timer = null;
  const session = {
    cast: true, connectionState: 'connecting', bytes: 0, delay: 0,
    close() {
      clearInterval(timer);
      ws.onclose = ws.onmessage = null;
      ws.close();
      video.removeAttribute('src');
      video.load();
      URL.revokeObjectURL(src);
    },
  };
  pc = session;
  castLast = null;
  video.srcObject = null;
  video.disableRemotePlayback = true; // ManagedMediaSource (iPhone) needs it
  video.src = src;

  // A SourceBuffer takes one append at a time; what piles up meanwhile goes
  // in as one piece.
  function feeder(sb) {
    const pending = [];
    const next = () => {
      if (sb.updating || !pending.length || ms.readyState !== 'open') return;
      let n = 0;
      for (const p of pending) n += p.length;
      const all = new Uint8Array(n);
      n = 0;
      for (const p of pending) { all.set(p, n); n += p.length; }
      pending.length = 0;
      try { sb.appendBuffer(all); } catch (err) {
        if (err.name === 'QuotaExceededError' && video.currentTime > 5) sb.remove(0, video.currentTime - 2);
      }
    };
    sb.addEventListener('updateend', next);
    return (bytes) => { pending.push(bytes); next(); };
  }

  function handle(m) {
    const kind = String.fromCharCode(m[0]);
    if (kind === 'H') { audio = m[1] === 1; return; }
    if (kind === 'V' || kind === 'A') {
      const codec = new TextDecoder().decode(m.subarray(2, 2 + m[1]));
      const seg = m.subarray(2 + m[1]);
      if (kind === 'A') {
        if (!audio) return;
        if (feedAudio) return void feedAudio(seg);
        audioInit = { codec, seg }; // set up together with the video, below
        return;
      }
      if (!vsb) {
        // Browsers take no new SourceBuffer once one has data: add both first.
        vsb = ms.addSourceBuffer(`video/mp4; codecs="${codec}"`);
        feedVideo = feeder(vsb);
        const a = audioInit && `audio/mp4; codecs="${audioInit.codec}"`;
        if (a && MS.isTypeSupported(a)) { asb = ms.addSourceBuffer(a); feedAudio = feeder(asb); feedAudio(audioInit.seg); }
      }
      feedVideo(seg);
      return;
    }
    if (kind === 'v' && feedVideo) {
      feedVideo(m.subarray(1));
      if (session.connectionState !== 'connected') started();
    }
    if (kind === 'a') {
      if (feedAudio) feedAudio(m.subarray(1));
      else if (!audio) restart(); // the host turned sound on: start over with it
    }
  }

  function started() {
    session.connectionState = 'connected';
    watched = true;
    watchTicket(session); watchWatchers(session); watchThrows(session);
    setState('live', 'live');
    show(null);
    applyVolume();
    play();
  }

  // Start over with a fresh MediaSource when the stream changed under us.
  // The host frees our ticket when the old WebSocket closes, so wait a moment.
  function restart() {
    if (session !== pc) return;
    teardown();
    setTimeout(() => watchViaLink(mine), 1000);
  }

  ms.addEventListener('sourceopen', () => {
    open = true;
    for (const m of queue.splice(0)) handle(m);
  });
  ws.onmessage = (e) => {
    if (session !== pc) return;
    const m = new Uint8Array(e.data);
    session.bytes += m.length;
    if (!open) return void queue.push(m);
    try { handle(m); } catch (err) { console.error(err); restart(); }
  };
  ws.onclose = () => {
    if (session !== pc) return;
    if (!session.bytes && tries < 3) { // the host may still be letting go of our last try
      teardown();
      return void setTimeout(() => watchViaLink(mine, tries + 1), 1000);
    }
    lost(session.connectionState === 'connected');
  };

  // Stay near the live edge, forget what's been played, and start over if
  // the picture is stuck while data keeps arriving (e.g. the sound stopped).
  let lastTime = -1, stuckSince = 0;
  timer = setInterval(() => {
    const b = video.buffered;
    if (!b.length || session.connectionState !== 'connected') return;
    const start = b.start(b.length - 1), end = b.end(b.length - 1), now = performance.now();
    session.delay = end - video.currentTime;
    if (video.currentTime < start || session.delay > 1.5) video.currentTime = Math.max(start, end - 0.4);
    if (video.paused && $('tapToPlay').hidden) play();
    if (video.currentTime === lastTime && session.delay > 1) {
      if (!stuckSince) stuckSince = now;
      else if (now - stuckSince > 4000) restart();
    } else stuckSince = 0;
    lastTime = video.currentTime;
    for (const sb of [vsb, asb]) {
      if (sb && !sb.updating && sb.buffered.length && video.currentTime - sb.buffered.start(0) > 30) {
        try { sb.remove(0, video.currentTime - 10); } catch { /* busy: next time */ }
      }
    }
  }, 500);
}

let castLast = null;
function castStats(s) {
  const now = performance.now();
  const q = video.getVideoPlaybackQuality ? video.getVideoPlaybackQuality() : null;
  const frames = q ? q.totalVideoFrames : 0;
  const kbps = castLast ? ((s.bytes - castLast.bytes) * 8) / (now - castLast.t) : 0;
  const fps = castLast ? ((frames - castLast.frames) * 1000) / (now - castLast.t) : 0;
  castLast = { t: now, bytes: s.bytes, frames };
  renderQuality({ frameWidth: video.videoWidth, frameHeight: video.videoHeight, framesPerSecond: fps }, kbps, null);
  $('qPing').textContent = 'via link';
  qualityEl.className = 'ok';
  if (statsEl.hidden) return;
  statsEl.textContent = [
    `video   ${video.videoWidth || '?'}x${video.videoHeight || '?'} @ ${Math.round(fps)} fps`,
    `bitrate ${(kbps / 1000).toFixed(2)} Mbps`,
    `delay   ${Math.round(s.delay * 1000)} ms buffered`,
    'path    through the link (no direct path between your networks)',
    `dropped ${q ? q.droppedVideoFrames : 0} frames`,
  ].join('\n');
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
  thrower.hidden = false;
}

// "Ana joined!" in a speech bubble for a few seconds.
const joinedEl = $('joined');
function joinedBubble(name) {
  const b = document.createElement('div');
  b.className = 'bubble';
  b.textContent = `${name} joined!`; // names come from other people: never as HTML
  joinedEl.append(b);
  while (joinedEl.children.length > 3) joinedEl.firstChild.remove();
  setTimeout(() => { b.classList.add('leaving'); setTimeout(() => b.remove(), 400); }, 4000);
}

// Long-polls the watcher list; rings and shows a bubble when someone new
// starts watching. Someone back within a minute of dropping out (a network
// blip) reconnects quietly.
async function watchWatchers(conn) {
  let version = '', known = null, names = new Set();
  const gone = new Map(); // name → when they left
  while (conn === pc) {
    let data;
    try {
      const r = await fetch(`${base}watchers?ticket=${ticket}&since=${version}`, { cache: 'no-store' });
      if (!r.ok) throw new Error(r.status);
      data = await r.json();
    } catch { if (conn !== pc) return; await delay(3000); continue; }
    if (conn !== pc) return;
    const ids = new Set(data.watchers.map((w) => w.name + '|' + w.since));
    const now = Date.now(), current = new Set(data.watchers.map((w) => w.name));
    for (const n of names) if (!current.has(n)) gone.set(n, now);
    if (known) {
      const fresh = data.watchers.filter((w) => !w.you && !known.has(w.name + '|' + w.since)
        && !(now - (gone.get(w.name) || 0) < 60000) && !names.has(w.name));
      if (fresh.length) playEffect('join-sound.wav');
      fresh.forEach((w) => joinedBubble(w.name));
    }
    known = ids;
    names = current;
    version = data.version;
    renderWatchers(data.watchers);
  }
}

// Sound effects play at the viewer's own volume and can overlap (a volley of
// throws). Web Audio where there is one; a plain audio element otherwise.
function loadEffect(file) {
  if (!fx || !file) return null;
  if (!fxBuffers.has(file)) {
    fxBuffers.set(file, fetch(base + file).then((r) => r.arrayBuffer())
      .then((b) => new Promise((ok, bad) => fx.decodeAudioData(b, ok, bad))) // callback form for older Safari
      .catch(() => { fxBuffers.delete(file); return null; }));
  }
  return fxBuffers.get(file);
}

async function playEffect(file) {
  if (!file || muted || volume === 0) return;
  try {
    if (fx) {
      const buf = await loadEffect(file);
      if (!buf) return;
      const src = fx.createBufferSource();
      src.buffer = buf;
      src.connect(fxGain);
      src.start();
    } else {
      const a = new Audio(base + file);
      a.volume = Math.min(1, Math.pow(volume / 100, 2));
      await a.play();
    }
  } catch { /* a sound effect isn't worth an error */ }
}

// --- Emoji throws ----------------------------------------------------------

const thrower = $('thrower'), throwBtn = $('throwBtn'), throwsEl = $('throws');
const emojiPick = $('emojiPick'), emojiMenu = $('emojiMenu');
throwBtn.textContent = store.get('emoji', '🍿');

// Throws the emoji on the button.
throwBtn.addEventListener('click', async (e) => {
  e.stopPropagation();
  closeEmojiMenu();
  const emoji = throwBtn.textContent;
  let ok = false;
  try {
    const r = await fetch(base + 'throw', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ticket, emoji }),
    });
    ok = r.ok;
  } catch { /* offline: nothing flies */ }
  if (!ok) bounce('nope');
});

function bounce(cls) {
  throwBtn.classList.remove('picked', 'nope');
  void throwBtn.offsetWidth; // restart the animation
  throwBtn.classList.add(cls);
}

// The arrow opens a picker; the choice sticks (and is remembered) until changed.
emojiPick.addEventListener('click', (e) => {
  e.stopPropagation();
  if (!emojiMenu.hidden) return closeEmojiMenu();
  emojiMenu.replaceChildren(...emojis.map((emoji) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = emoji;
    b.setAttribute('role', 'menuitemradio');
    b.setAttribute('aria-checked', emoji === throwBtn.textContent);
    b.addEventListener('click', (ev) => {
      ev.stopPropagation();
      throwBtn.textContent = emoji;
      store.set('emoji', emoji);
      closeEmojiMenu();
      bounce('picked');
      throwBtn.focus();
    });
    return b;
  }));
  emojiMenu.hidden = false;
  emojiPick.setAttribute('aria-expanded', 'true');
  (emojiMenu.querySelector('[aria-checked="true"]') || emojiMenu.firstChild).focus();
});
emojiMenu.addEventListener('click', (e) => e.stopPropagation());
document.addEventListener('click', closeEmojiMenu);
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !emojiMenu.hidden) { closeEmojiMenu(); emojiPick.focus(); } });

function closeEmojiMenu() {
  emojiMenu.hidden = true;
  emojiPick.setAttribute('aria-expanded', 'false');
}

// Long-polls the host for throws (everyone's, ours included) and animates them.
async function watchThrows(conn) {
  let seq = '';
  while (conn === pc) {
    let data;
    try {
      const r = await fetch(`${base}throws?ticket=${ticket}&since=${seq}`, { cache: 'no-store' });
      if (!r.ok) throw new Error(r.status);
      data = await r.json();
    } catch { if (conn !== pc) return; await delay(3000); continue; }
    if (conn !== pc) return;
    seq = data.seq;
    // After a sleeping tab wakes up, only the last few are worth showing.
    data.throws.slice(-8).forEach((t, i) => setTimeout(() => animateThrow(t), i * 90));
  }
}

const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');

// An emoji flies up from the bottom, grows as it comes at the screen, smacks
// into it at (t.x, t.y) with a burst, then slides off. The host picks how long
// it's in the air (0.3–0.8 s), so everyone hears the hit at the same moment.
function animateThrow(t) {
  if (throwsEl.childElementCount >= 30) return;
  const w = stage.clientWidth, h = stage.clientHeight;
  const el = document.createElement('div');
  el.className = 'throw';
  const face = document.createElement('div');
  face.className = 'emoji';
  face.textContent = t.emoji;
  const tag = document.createElement('div');
  tag.className = 'tag';
  tag.textContent = t.name; // names come from other people: never as HTML
  const burst = document.createElement('div');
  burst.className = 'burst';
  el.append(burst, face, tag);
  throwsEl.append(el);

  const half = el.offsetWidth / 2;
  const tx = t.x * w - half, ty = t.y * h - 24;
  const sx = tx + (Math.random() - 0.5) * w * 0.4, sy = h + 40;
  const spin = Math.random() < 0.5 ? -1 : 1;
  const flight = t.flight || 700;
  const turn = 320 * spin;

  playEffect(t.throw);
  if (reducedMotion.matches) {
    // No flying: it just appears where it hits, on time with the hit sound.
    el.animate([{ transform: `translate(${tx}px, ${ty}px)`, opacity: 0 }, { transform: `translate(${tx}px, ${ty}px)`, opacity: 0 }],
      { duration: flight, fill: 'forwards' });
    face.style.transform = `scale(2.2) rotate(${turn}deg)`;
    tag.style.transform = 'translateY(30px)';
  } else el.animate([
    { transform: `translate(${sx}px, ${sy}px)` },
    { transform: `translate(${(sx + tx) / 2}px, ${Math.min(sy, ty) - h * 0.18}px)`, offset: 0.55 },
    { transform: `translate(${tx}px, ${ty}px)` },
  ], { duration: flight, easing: 'cubic-bezier(.45,0,.75,1)', fill: 'forwards' });
  if (!reducedMotion.matches) {
    face.animate([
      { transform: `scale(.4) rotate(${-40 * spin}deg)` },
      { transform: `scale(2.2) rotate(${turn}deg)` },
    ], { duration: flight, easing: 'ease-in', fill: 'forwards' });
    // Keep the name just under the emoji as it grows.
    tag.animate([{ transform: 'translateY(-16px)' }, { transform: 'translateY(30px)' }],
      { duration: flight, easing: 'ease-in', fill: 'forwards' });
  }

  setTimeout(() => {
    playEffect(t.hit);
    el.classList.add('hit');
    face.animate([
      { transform: `scale(2.2) rotate(${turn}deg)` },
      { transform: `scale(2.7, 1.6) rotate(${turn}deg)` },
      { transform: `scale(2, 2.4) rotate(${turn}deg)` },
      { transform: `scale(2.2) rotate(${turn}deg)` },
    ], { duration: 260, easing: 'ease-out', fill: 'forwards' });
    el.animate([
      { transform: `translate(${tx}px, ${ty}px)`, opacity: 1 },
      { transform: `translate(${tx}px, ${ty}px)`, opacity: 1, offset: 0.55 },
      { transform: `translate(${tx}px, ${ty + 80}px)`, opacity: 0 },
    ], { duration: 1400, easing: 'ease-in', fill: 'forwards' }).onfinish = () => el.remove();
  }, flight);
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
  thrower.hidden = true;
  closeEmojiMenu();
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
  const AC = window.AudioContext || window.webkitAudioContext;
  if (!fx && AC) {
    fx = actx || new AC();
    fxGain = fx.createGain();
    fxGain.connect(fx.destination);
    loadEffect('join-sound.wav');
  }
  if (fx) fx.resume();
  if (!video.srcObject && !video.getAttribute('src')) video.srcObject = new MediaStream();
  video.play().catch(() => {});
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
// Any press counts, not just movement (clicking the throw button over and
// over keeps the bar up). Capture phase, so buttons that stop their clicks
// from bubbling still count.
['mousemove', 'pointerdown', 'touchstart', 'keydown', 'wheel'].forEach((ev) => stage.addEventListener(ev, wake, { passive: true, capture: true }));

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
  if (pc && pc.cast && pc.connectionState === 'connected') return castStats(pc);
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

// The host's own tab (opened from the tray menu) ends in #host=<key>: it
// fills in their name and gets in without asking. The key is kept for this
// tab's reloads, and dropped from the address bar so a link copied from
// there is plain.
let hostKey = '';
try { hostKey = sessionStorage.getItem('hostKey') || ''; } catch { /* no storage: no reloads as host */ }
const hostMark = /^#host=([0-9a-f]+)$/.exec(location.hash);
if (hostMark) {
  hostKey = hostMark[1];
  try { sessionStorage.setItem('hostKey', hostKey); } catch { /* fine */ }
  history.replaceState(null, '', location.pathname + location.search);
}
const fromHost = !!hostKey;

// The tab title alternates "Movie night" / "Calvin streams" every 2 s (0.5 Hz).
fetch(base + 'config', { cache: 'no-store' }).then((r) => r.json()).then((cfg) => {
  if (!cfg.host) return;
  if (fromHost && !nameInput.value.trim()) nameInput.value = cfg.host;
  const titles = ['Movie night', `${cfg.host} streams`];
  let i = 0;
  setInterval(() => { document.title = titles[i = 1 - i]; }, 2000);
}).catch(() => { /* keeps "Movie night" */ });

nameInput.value = store.get('name', '');
show('askForm');
setState('offline', 'bad');
applyVolume();
