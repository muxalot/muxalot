// UI only. Keys, signing and all network I/O live in Go; this page calls the
// fixed Service surface (service.go). Server-supplied text is only ever placed
// with textContent, never as HTML.
import { Call, Events } from '/wails/runtime.js';

const call = (m, ...a) => Call.ByName('main.Service.' + m, ...a);
const $ = (id) => document.getElementById(id);
const NAME_RE = /^[A-Za-z0-9_-]{1,32}$/;

let servers = [];
let cur = null; // current server id
const tabs = new Map(); // "server/name" -> tab
let active = null; // key of the visible tab
let fontSize = 14;
let confirming = false; // one OSC 52 prompt at a time
const CHIP_COLORS = { Blue: '#3465a4', Green: '#73d216', Orange: '#f57900', Purple: '#75507b', Red: '#cc0000', Yellow: '#edd400' };
const tabColors = new Map(); // "server/name" -> hex color

const msg = (e) => (e && e.message) || String(e);

// ---------------------------------------------------------------- banner / modal
let bannerTimer;
function banner(text, kind = 'info') {
  const b = $('banner');
  b.textContent = text;
  b.className = kind === 'error' ? 'error' : '';
  b.hidden = false;
  clearTimeout(bannerTimer);
  bannerTimer = setTimeout(() => { b.hidden = true; }, kind === 'error' ? 10000 : 5000);
}
const fail = (e) => banner(msg(e), 'error');

let modalChain = Promise.resolve();
// ask() shows a modal and resolves {value, text}; value is null when dismissed. Modals queue.
function ask(opts) {
  const run = () => new Promise((resolve) => {
    const { title, body = '', input = null, initial = '', buttons } = opts;
    const back = document.activeElement;
    $('m-title').textContent = title;
    $('m-body').textContent = body;
    $('m-body').hidden = !body;
    const inp = $('m-input');
    inp.hidden = input === null;
    inp.type = input === 'password' ? 'password' : 'text';
    inp.value = initial;
    const row = $('m-buttons');
    row.replaceChildren();
    const primary = buttons.find((b) => b.primary);
    const done = (value) => {
      document.removeEventListener('keydown', onKey, true);
      $('modal').hidden = true;
      if (back && back.focus) back.focus();
      resolve({ value, text: inp.value });
    };
    const onKey = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); done(null); }
      else if (e.key === 'Enter' && input !== null && primary) { e.preventDefault(); e.stopPropagation(); done(primary.value); }
    };
    let first;
    for (const b of buttons) {
      const el = document.createElement('button');
      el.type = 'button';
      el.textContent = b.label;
      el.addEventListener('click', () => done(b.value));
      row.append(el);
      if (b.primary) first = el;
    }
    document.addEventListener('keydown', onKey, true);
    $('modal').hidden = false;
    (input !== null ? inp : first || row.firstChild).focus();
  });
  modalChain = modalChain.then(run, run);
  return modalChain;
}

// ---------------------------------------------------------------- byte helpers
function b64(bytes) {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  return btoa(s);
}
const fromB64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
// Wails delivers the emitted value as event.data
const payload = (ev) => (Array.isArray(ev.data) ? ev.data[0] : ev.data);

// ---------------------------------------------------------------- terminals
function fitTab(t) {
  if (t.el.style.display === 'none') return;
  try { t.fit.fit(); } catch (e) { /* not measurable yet */ }
}

function zoom(d) {
  fontSize = d === 0 ? 14 : Math.min(32, Math.max(8, fontSize + d));
  for (const t of tabs.values()) { t.term.options.fontSize = fontSize; fitTab(t); }
}

function keyHandler(t, e) {
  if (e.type !== 'keydown') return true;
  if (e.ctrlKey && e.shiftKey && e.code === 'KeyC') {
    const s = t.term.getSelection();
    if (s) call('SetClipboard', s).catch(fail);
    return false;
  }
  if (e.ctrlKey && e.shiftKey && e.code === 'KeyV') return false; // the browser paste event does the rest
  if (e.ctrlKey && !e.altKey) {
    if (e.key === '=' || e.key === '+') { zoom(1); return false; }
    if (e.key === '-') { zoom(-1); return false; }
    if (e.key === '0') { zoom(0); return false; }
  }
  return true;
}

// OSC 52: the terminal (any program, any file cat'ed) asks to set the clipboard. Always ask first.
function osc52(t, data) {
  const payloadB64 = data.slice(data.indexOf(';') + 1);
  if (confirming || payloadB64 === '?' || payloadB64 === '') return true;
  let text;
  try { text = new TextDecoder().decode(fromB64(payloadB64)); } catch (e) { return true; }
  if (!text) return true;
  confirming = true;
  const preview = text.length > 300 ? text.slice(0, 300) + '…' : text;
  ask({
    title: 'Copy to clipboard?',
    body: `The terminal wants to copy ${text.length} characters:\n\n${preview}`,
    buttons: [{ label: 'Deny', value: false }, { label: 'Copy', value: true, primary: true }],
  }).then((r) => {
    confirming = false;
    if (r.value) call('SetClipboard', text).catch(fail);
  });
  return true;
}

async function offerLink(url) {
  if (confirming || !/^https?:\/\//i.test(url)) return;
  confirming = true;
  const r = await ask({
    title: 'Terminal link',
    body: url.length > 300 ? url.slice(0, 300) + '…' : url,
    buttons: [{ label: 'Close', value: false }, { label: 'Copy link', value: 'copy' }, { label: 'Open', value: 'open', primary: true }],
  });
  confirming = false;
  if (r.value === 'open') call('OpenURL', url).catch(fail);
  else if (r.value === 'copy') call('SetClipboard', url).catch(fail);
}

async function doPaste(t, text) {
  if (!text) return;
  const bracketed = t.term.modes.bracketedPasteMode;
  if (!bracketed && /[\r\n]/.test(text)) {
    const lines = text.split(/\r\n|\r|\n/).length;
    const preview = text.length > 300 ? text.slice(0, 300) + '…' : text;
    const r = await ask({
      title: 'Paste multiple lines?',
      body: `${lines} lines will be typed and run immediately.\n\n${preview}`,
      buttons: [{ label: 'Cancel', value: false }, { label: 'Paste', value: true, primary: true }],
    });
    if (!r.value) return;
  }
  call('Paste', t.server, t.name, text, bracketed).catch(fail);
}

async function openTab(name) {
  const key = cur + '/' + name;
  if (tabs.has(key)) return select(key);
  const el = document.createElement('div');
  el.className = 'term';
  $('terms').append(el);
  const term = new Terminal({
    fontFamily: '"JetBrainsMono NFM", monospace',
    fontSize,
    cursorBlink: true,
    scrollback: 5000,
    allowProposedApi: true,
    // OSC 8 links and detected plain URLs come from terminal output; both are confirmed before opening.
    linkHandler: { activate: (e, text) => offerLink(text) },
    theme: { background: '#0b0f14', foreground: '#d8dee9', cursor: '#ffcc66' },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new Unicode11Addon.Unicode11Addon());
  term.unicode.activeVersion = '11';
  term.loadAddon(new WebLinksAddon.WebLinksAddon((e, text) => offerLink(text)));
  term.open(el);
  const t = { key, server: cur, name, el, term, fit, state: 'connecting', attached: false };
  tabs.set(key, t);

  term.onData((d) => call('Send', t.server, name, b64(new TextEncoder().encode(d))).catch(() => {}));
  term.onBinary((s) => call('Send', t.server, name, b64(Uint8Array.from(s, (c) => c.charCodeAt(0)))).catch(() => {}));
  term.onResize(({ cols, rows }) => { if (t.attached) call('Resize', t.server, name, cols, rows).catch(() => {}); });
  term.attachCustomKeyEventHandler((e) => keyHandler(t, e));
  term.parser.registerOscHandler(52, (data) => osc52(t, data));
  // Capture on the container: xterm.js also listens for paste on its textarea and would send
  // the text unsanitised. An ancestor's capture listener always runs before the target's.
  el.addEventListener('paste', (e) => {
    e.preventDefault();
    e.stopPropagation();
    doPaste(t, e.clipboardData.getData('text/plain'));
  }, true);

  select(key);
  // cell size was measured with the fallback font; re-measure once the real one loads
  document.fonts.load('14px "JetBrainsMono NFM"').then(() => {
    const ff = term.options.fontFamily;
    term.options.fontFamily = 'monospace';
    term.options.fontFamily = ff;
    fitTab(t);
  });
  try {
    await call('Attach', t.server, name, term.cols, term.rows);
    t.attached = true;
  } catch (e) {
    fail(e);
  }
  renderTabs();
}

function select(key) {
  active = key;
  for (const t of tabs.values()) t.el.style.display = t.key === key ? '' : 'none';
  renderTabs();
  const t = tabs.get(key);
  if (t) { fitTab(t); t.term.focus(); }
}

function renderTabs() {
  const nav = $('tabs');
  nav.replaceChildren();
  for (const t of tabs.values()) {
    if (t.server !== cur) continue;
    const tab = document.createElement('div');
    tab.className = 'tab' + (t.key === active ? ' on' : '');
    const c = tabColors.get(t.key);
    if (c) tab.style.borderBottom = '4px solid ' + c;
    const dot = document.createElement('span');
    dot.className = 'dot ' + t.state;
    dot.title = t.state;
    const name = document.createElement('button');
    name.className = 'name';
    name.textContent = t.name;
    name.addEventListener('click', () => select(t.key));
    name.addEventListener('contextmenu', (e) => { e.preventDefault(); showTabMenu(t, e); });
    const x = document.createElement('button');
    x.className = 'x';
    x.textContent = '×';
    x.title = 'Close';
    x.setAttribute('aria-label', 'Close ' + t.name);
    x.addEventListener('click', () => closeTab(t));
    tab.append(dot, name, x);
    nav.append(tab);
  }
}

async function closeTab(t) {
  const r = await ask({
    title: `Close "${t.name}"?`,
    body: 'Detach keeps the session running on the server. Kill ends it and everything in it.',
    buttons: [
      { label: 'Cancel', value: null },
      { label: 'Kill session', value: 'kill' },
      { label: 'Detach', value: 'detach', primary: true },
    ],
  });
  if (!r.value) return;
  try { await call('Detach', t.server, t.name, r.value === 'kill'); } catch (e) { fail(e); }
  dispose(t);
  if (active === t.key) {
    const next = [...tabs.values()].find((x) => x.server === cur);
    if (next) select(next.key); else { active = null; renderTabs(); }
  } else renderTabs();
}

function dispose(t) {
  t.term.dispose();
  t.el.remove();
  tabs.delete(t.key);
  tabColors.delete(t.key); // reappears for the same session name after refresh
}

// ---------------------------------------------------------------- tab context menu

let menuTab = null; // the tab the open context menu acts on

function showTabMenu(t, e) {
  select(t.key); // GNOME behavior: opening the menu focuses that tab
  menuTab = t;
  buildColorSub();
  const m = $('tabmenu');
  m.hidden = false;
  m.style.left = Math.min(e.clientX, innerWidth - m.offsetWidth - 8) + 'px';
  m.style.top = Math.min(e.clientY, innerHeight - m.offsetHeight - 8) + 'px';
}

function hideTabMenu() {
  menuTab = null;
  $('tabmenu').hidden = true;
}

function buildColorSub() {
  const sub = $('colorsub');
  sub.replaceChildren();
  const cur = menuTab ? tabColors.get(menuTab.key) || '' : '';
  const add = (label, hex) => {
    const b = document.createElement('button');
    b.className = 'sw';
    b.dataset.color = hex;
    b.textContent = (hex && cur === hex ? '✓ ' : '') + label;
    const chip = document.createElement('span');
    chip.className = 'chip';
    if (hex) chip.style.background = hex;
    b.prepend(chip);
    sub.append(b);
  };
  add('No color', '');
  for (const [n, hex] of Object.entries(CHIP_COLORS)) add(n, hex);
}

$('tabmenu').addEventListener('click', (e) => {
  const act = e.target.closest('button')?.dataset?.act;
  if (!act || !menuTab || act === 'color') return; // color opens the hover submenu
  const t = menuTab;
  hideTabMenu();
  if (act === 'rename') return renameTab(t);
  const all = [...tabs.values()].filter((x) => x.server === t.server);
  const i = all.indexOf(t);
  const list = act === 'close-others' ? all.filter((x) => x !== t)
    : act === 'close-left' ? all.slice(0, i)
    : all.slice(i + 1);
  bulkClose(t, list);
});

$('colorsub').addEventListener('click', async (e) => {
  if (!menuTab) return;
  const hex = e.target.closest('.sw')?.dataset?.color;
  if (hex === undefined || !menuTab) return;
  const t = menuTab;
  hideTabMenu();
  if (hex) tabColors.set(t.key, hex); else tabColors.delete(t.key);
  try { await call('SetTabColor', t.server, t.name, hex); } catch (err) { fail(err); }
  renderTabs();
});

document.addEventListener('click', (e) => {
  if (!$('tabmenu').hidden && !e.target.closest('#tabmenu') && !e.target.closest('#tabs .name')) hideTabMenu();
});

async function bulkClose(target, list) {
  if (!list.length) return;
  const r = await ask({
    title: `Detach ${list.length} tab${list.length > 1 ? 's' : ''}?`,
    body: 'Sessions keep running on the server and come back when reopened or refreshed.',
    buttons: [{ label: 'Cancel', value: null }, { label: 'Detach', value: true, primary: true }],
  });
  if (!r.value) return;
  for (const x of list) {
    try { await call('Detach', x.server, x.name, false); } catch (err) { fail(err); }
    dispose(x);
  }
  renderTabs();
  select(target.key);
}

async function renameTab(t) {
  for (;;) {
    const r = await ask({
      title: `Rename "${t.name}"`,
      body: 'Letters, digits, - and _ (max 32). Renames the tmux session on the server.',
      input: 'text',
      initial: t.name,
      buttons: [{ label: 'Cancel', value: null }, { label: 'Rename', value: true, primary: true }],
    });
    if (!r.value) return;
    if (!NAME_RE.test(r.text)) { fail('Invalid session name'); continue; }
    if (r.text === t.name) return;
    try { await call('Rename', t.server, t.name, r.text); } catch (e) { return fail(e); }
    const oldColor = tabColors.get(t.key);
    if (oldColor) { // keep the chip color across the rename, on disk too
      tabColors.delete(t.key);
      tabColors.set(t.server + '/' + r.text, oldColor);
      call('SetTabColor', t.server, t.name, '').catch(() => {});
      call('SetTabColor', t.server, r.text, oldColor).catch(() => {});
    }
    // the stream is keyed by the session name everywhere, so detach and reopen
    // (same reset as any reconnect; the tab moves to the end of the strip)
    await call('Detach', t.server, t.name, false).catch(() => {});
    dispose(t);
    await openTab(r.text);
  }
}

Events.On('term:data', (ev) => {
  const d = payload(ev);
  const t = tabs.get(d.server + '/' + d.session);
  if (t) t.term.write(fromB64(d.b64));
});

Events.On('term:state', (ev) => {
  const d = payload(ev);
  const t = tabs.get(d.server + '/' + d.session);
  if (!t) return;
  // tmux repaints the whole screen on reattach, so start clean after a drop
  if (d.state === 'connected' && t.state === 'reconnecting') t.term.reset();
  t.state = d.state;
  if (d.state === 'exited') banner(`Session "${t.name}" ended`);
  renderTabs();
});

new ResizeObserver(() => { const t = tabs.get(active); if (t) fitTab(t); }).observe($('terms'));
// dropping a file on the window would otherwise navigate the webview to it
for (const ev of ['dragover', 'drop']) document.addEventListener(ev, (e) => e.preventDefault());

// ---------------------------------------------------------------- servers
function fillServers() {
  const sel = $('server');
  sel.replaceChildren();
  for (const s of servers) {
    const o = document.createElement('option');
    o.value = s.id;
    o.textContent = s.name;
    sel.append(o);
  }
  if (cur) sel.value = cur;
}

async function unlockPrompt(id) {
  for (;;) {
    const r = await ask({
      title: 'Unlock key',
      body: 'Enter the passphrase for this server\'s key.',
      input: 'password',
      buttons: [{ label: 'Cancel', value: null }, { label: 'Unlock', value: true, primary: true }],
    });
    if (!r.value) return false;
    try { await call('Unlock', id, r.text); return true; } catch (e) { fail(e); }
  }
}

async function loadServer(id) {
  cur = id;
  $('server').value = id;
  for (const t of tabs.values()) t.el.style.display = 'none';
  let names = [];
  try {
    names = await call('Sessions', id);
    for (const [k, v] of Object.entries(await call('TabColors', id) || {})) if (v) tabColors.set(id + '/' + k, v);
  } catch (e) {
    if (/locked/i.test(msg(e)) && await unlockPrompt(id)) return loadServer(id);
    fail(e);
  }
  const have = new Set([...tabs.values()].filter((t) => t.server === id).map((t) => t.name));
  for (const n of names) if (!have.has(n)) await openTab(n);
  if (![...tabs.values()].some((t) => t.server === id)) await openTab('main');
  const first = [...tabs.values()].find((t) => t.server === id);
  if (first) select(first.key);
}

function showPair() {
  $('app').hidden = true;
  $('list').hidden = true;
  $('pair').hidden = false;
  $('p-cancel').hidden = servers.length === 0;
  $('p-url').focus();
}

function showApp() {
  $('pair').hidden = true;
  $('list').hidden = true;
  $('app').hidden = false;
}

// Landing view. Open tabs stay alive (hidden inside #terms); reopening a card
// runs the normal loadServer flow, which re-attaches to them.
function showList() {
  $('pair').hidden = true;
  $('app').hidden = true;
  const box = $('list-cards');
  box.replaceChildren();
  for (const s of servers) {
    const card = document.createElement('div');
    card.className = 'card';
    card.tabIndex = 0;
    card.role = 'button';
    const meta = document.createElement('div');
    meta.className = 'meta';
    const b = document.createElement('b');
    b.textContent = s.name;
    const u = document.createElement('span');
    u.textContent = s.url;
    meta.append(b, u);
    card.append(meta);
    const open = () => { showApp(); loadServer(s.id); };
    card.addEventListener('click', open);
    card.addEventListener('keydown', (e) => { if (e.key === 'Enter' && e.target === card) open(); });
    if (s.id === cur) card.classList.add('cur');
    const forgetB = document.createElement('button');
    forgetB.textContent = 'Forget';
    forgetB.addEventListener('click', (e) => { e.stopPropagation(); forgetServer(s); });
    card.append(forgetB);
    box.append(card);
  }
  $('list').hidden = false;
}

async function forgetServer(s) {
  const r = await ask({
    title: `Forget ${s.name}?`,
    body: 'This deletes this device\'s key. To use the server again you must pair again. Revoke the device on the server with `muxalot-agent revoke`.',
    buttons: [{ label: 'Cancel', value: null }, { label: 'Forget', value: true, primary: true }],
  });
  if (!r.value) return;
  try { await call('Forget', s.id); } catch (e) { return fail(e); }
  for (const t of [...tabs.values()]) if (t.server === s.id) dispose(t);
  if (cur === s.id) cur = null;
  servers = await call('Servers');
  fillServers();
  if (servers.length) showList(); else showPair();
}

$('server').addEventListener('change', (e) => loadServer(e.target.value));
for (const el of document.querySelectorAll('.add-server')) el.addEventListener('click', showPair);
$('servers').addEventListener('click', showList);
$('p-cancel').addEventListener('click', () => (servers.length ? showList() : showPair()));
$('new-tab').addEventListener('click', async () => {
  for (;;) {
    const r = await ask({
      title: 'New session',
      body: 'Letters, digits, - and _ (max 32). An existing name attaches to it.',
      input: 'text',
      initial: 's' + (tabs.size + 1),
      buttons: [{ label: 'Cancel', value: null }, { label: 'Open', value: true, primary: true }],
    });
    if (!r.value) return;
    if (NAME_RE.test(r.text)) return openTab(r.text);
    fail('Invalid session name');
  }
});

// ---------------------------------------------------------------- pairing
for (const r of document.querySelectorAll('input[name=mode]')) {
  r.addEventListener('change', () => { $('p-pass-row').hidden = document.querySelector('input[name=mode]:checked').value !== 'passphrase'; });
}

$('pair-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  let url = $('p-url').value.trim();
  let code = $('p-code').value.trim();
  if (url.startsWith('muxalot://')) { // the link the agent's QR code carries
    try {
      const u = new URL(url);
      url = u.searchParams.get('url') || '';
      code = u.searchParams.get('code') || code;
    } catch (err) { return fail('Invalid pairing link'); }
  }
  const mode = document.querySelector('input[name=mode]:checked').value;
  $('p-go').disabled = true;
  try {
    const s = await call('Pair', url, code, $('p-name').value, mode, $('p-pass').value);
    $('p-code').value = '';
    $('p-pass').value = '';
    servers = await call('Servers');
    fillServers();
    showApp();
    banner('Paired with ' + s.name);
    await loadServer(s.id);
  } catch (err) {
    fail(err);
  } finally {
    $('p-go').disabled = false;
  }
});

// ---------------------------------------------------------------- files
let fpath = '';

async function listFiles(path) {
  try {
    const r = await call('Ls', cur, path);
    fpath = r.path;
    $('f-path').textContent = fpath;
    const ul = $('f-list');
    ul.replaceChildren();
    const row = (label, cls, onClick, size, dl) => {
      const li = document.createElement('li');
      const n = document.createElement('span');
      n.className = 'name ' + cls;
      n.textContent = label;
      if (onClick) n.addEventListener('click', onClick);
      li.append(n);
      if (size) { const s = document.createElement('span'); s.className = 'size'; s.textContent = size; li.append(s); }
      if (dl) { const b = document.createElement('button'); b.textContent = 'Download'; b.addEventListener('click', dl); li.append(b); }
      ul.append(li);
    };
    row('..', 'dir', () => listFiles(fpath.replace(/\/[^/]*$/, '') || '/'));
    for (const f of r.entries || []) {
      const p = fpath.replace(/\/$/, '') + '/' + f.name;
      if (f.dir) row(f.name + '/', 'dir', () => listFiles(p));
      else row(f.name, 'file', null, f.size + ' B', () => { $('f-status').textContent = ''; call('Download', cur, p).then(() => banner('Saved ' + f.name)).catch(fail); });
    }
  } catch (e) { fail(e); }
}

$('files-btn').addEventListener('click', () => { $('files').hidden = false; listFiles(fpath || ''); });
$('f-close').addEventListener('click', () => { $('files').hidden = true; const t = tabs.get(active); if (t) t.term.focus(); });
$('f-up').addEventListener('click', () => {
  call('Upload', cur, fpath).then(() => { $('f-status').textContent = ''; listFiles(fpath); }).catch(fail);
});
Events.On('xfer:progress', (ev) => {
  const d = payload(ev);
  $('f-status').textContent = `${d.name}: ${d.total > 0 ? Math.floor(100 * d.done / d.total) + '%' : d.done + ' B'}`;
});

// ---------------------------------------------------------------- start
(async () => {
  try {
    servers = await call('Servers');
    fillServers();
    if (!servers.length) return showPair();
    showList(); // land on the chooser; nothing loads until a server is picked
  } catch (e) {
    fail(e);
  }
})();
