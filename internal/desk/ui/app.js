// Manifesto desk: a single-page app over desk's backend. Every request runs
// as the signed-in user; Kubernetes RBAC decides what they may do.

import * as api from './api.js';
import { printName, upload } from './media.js';

const SECTION_LABEL = 'section.manifesto.davy.tw/name';
const STAMP_PREFIX = 'stamp.manifesto.davy.tw/';
const STATES = ['Draft', 'Published', 'Unlisted', 'Retracted'];

// h builds DOM. Text always goes through textContent, never innerHTML.
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'value') el.value = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

const main = () => document.getElementById('main');
let current = new AbortController(); // aborted when the view changes

function show(...nodes) {
  current.abort();
  current = new AbortController();
  main().replaceChildren(...nodes);
  return current.signal;
}

function notice(text, kind = 'error') {
  const el = h('p', { class: `notice ${kind}`, role: kind === 'error' ? 'alert' : 'status' }, text);
  return el;
}

function failure(err) {
  if (err.status === 401) return notice('Your session ended. Sign in again.');
  if (err.status === 403) return notice(`You do not have access: ${err.message}`);
  return notice(err.message || String(err));
}

// --- namespace and routing --------------------------------------------------

const RECENT = 'manifesto.recentNamespaces';
function recentNamespaces() {
  try {
    return JSON.parse(localStorage.getItem(RECENT) || '[]');
  } catch {
    return [];
  }
}
function remember(ns) {
  try {
    localStorage.setItem(RECENT, JSON.stringify([ns, ...recentNamespaces().filter((n) => n !== ns)].slice(0, 8)));
  } catch {
    // storage unavailable
  }
}

function route() {
  const parts = location.hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
  if (parts[0] !== 'ns' || !parts[1]) return chooseNamespace();
  const ns = parts[1];
  remember(ns);
  document.getElementById('ns').value = ns;
  document.getElementById('nav-dispatches').href = `#/ns/${encodeURIComponent(ns)}/dispatches`;
  document.getElementById('nav-media').href = `#/ns/${encodeURIComponent(ns)}/media`;
  switch (parts[2]) {
    case 'media':
      return mediaView(ns);
    case 'new':
      return editorView(ns, null);
    case 'dispatches':
      if (parts[3]) return editorView(ns, parts[3]);
      return dispatchList(ns);
    default:
      location.hash = `#/ns/${encodeURIComponent(ns)}/dispatches`;
  }
}

function chooseNamespace() {
  const recent = recentNamespaces();
  show(
    h('h1', {}, 'Choose a publication'),
    h('p', {}, 'Enter the namespace of the Gazette you write for.'),
    recent.length ? h('ul', { class: 'recent' }, recent.map((n) => h('li', {}, h('a', { href: `#/ns/${encodeURIComponent(n)}/dispatches` }, n)))) : null,
  );
}

// --- dispatches ----------------------------------------------------------------

function fmtDate(s) {
  return s ? new Date(s).toLocaleString() : '';
}

function dispatchList(ns) {
  const tbody = h('tbody');
  const status = h('div');
  const signal = show(
    h('div', { class: 'toolbar' }, h('h1', {}, 'Dispatches'), h('a', { class: 'button', href: `#/ns/${encodeURIComponent(ns)}/new` }, 'New dispatch')),
    status,
    h('table', {}, h('thead', {}, h('tr', {}, h('th', {}, 'Title'), h('th', {}, 'Phase'), h('th', {}, 'Published'), h('th', {}, 'Gazette'))), tbody),
  );
  const items = new Map();
  const render = () => {
    const rows = [...items.values()].sort((a, b) => (b.metadata.creationTimestamp || '').localeCompare(a.metadata.creationTimestamp || ''));
    tbody.replaceChildren(
      ...rows.map((d) =>
        h('tr', {},
          h('td', {}, h('a', { href: `#/ns/${encodeURIComponent(ns)}/dispatches/${encodeURIComponent(d.metadata.name)}` }, d.spec.title || d.metadata.name)),
          h('td', {}, h('span', { class: `phase ${(d.status?.phase || 'unknown').toLowerCase()}` }, d.status?.phase || '…')),
          h('td', {}, fmtDate(d.status?.publishedAt)),
          h('td', {}, d.spec.gazetteRef),
        ),
      ),
    );
    if (!rows.length) tbody.append(h('tr', {}, h('td', { colspan: 4 }, 'No dispatches yet.')));
  };
  api.watch(ns, 'dispatches', ({ type, object }) => {
    if (type === 'DELETED') items.delete(object.metadata.name);
    else items.set(object.metadata.name, object);
    render();
  }, signal).catch((err) => status.replaceChildren(failure(err)));
  render();
}

function conditions(obj) {
  return h('ul', { class: 'conditions' },
    (obj?.status?.conditions || []).map((c) =>
      h('li', { class: c.status === 'True' ? 'ok' : 'bad' }, h('strong', {}, `${c.type}: `), c.status === 'True' ? c.reason : `${c.reason} — ${c.message}`),
    ),
  );
}

function toLocalInput(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

async function editorView(ns, name) {
  const signal = show(h('p', {}, 'Loading…'));
  let dispatch = null;
  let latest = null; // newest version seen on the watch
  let gazettes = [];
  let prints = [];
  try {
    [gazettes, prints] = await Promise.all([api.list(ns, 'gazettes'), api.list(ns, 'prints')]);
    if (name) dispatch = await api.get(ns, 'dispatches', name);
  } catch (err) {
    show(failure(err));
    return;
  }
  if (signal.aborted) return;

  const labels = dispatch?.metadata.labels || {};
  const f = {
    name: h('input', { id: 'f-name', required: true, pattern: '[a-z0-9]([-a-z0-9]*[a-z0-9])?', maxlength: 63, value: dispatch?.metadata.name || '', disabled: !!dispatch }),
    title: h('input', { id: 'f-title', required: true, maxlength: 256, value: dispatch?.spec.title || '' }),
    summary: h('input', { id: 'f-summary', maxlength: 1024, value: dispatch?.spec.summary || '' }),
    gazette: h('select', { id: 'f-gazette' }, gazettes.map((g) => h('option', { value: g.metadata.name }, g.spec.title || g.metadata.name))),
    state: h('select', { id: 'f-state' }, STATES.map((s) => h('option', { value: s }, s))),
    publishAt: h('input', { id: 'f-publishAt', type: 'datetime-local', value: toLocalInput(dispatch?.spec.publishAt) }),
    section: h('input', { id: 'f-section', pattern: '[a-z0-9]([-a-z0-9]*[a-z0-9])?', value: labels[SECTION_LABEL] || '' }),
    stamps: h('input', { id: 'f-stamps', placeholder: 'kubernetes, go', value: Object.keys(labels).filter((k) => k.startsWith(STAMP_PREFIX)).map((k) => k.slice(STAMP_PREFIX.length)).join(', ') }),
    cover: h('select', { id: 'f-cover' }, h('option', { value: '' }, '(none)'), prints.map((p) => h('option', { value: `print:${p.metadata.name}` }, p.metadata.name))),
    body: h('textarea', { id: 'f-body', rows: 24, spellcheck: 'true' }, dispatch?.spec.content?.body || ''),
  };
  f.gazette.value = dispatch?.spec.gazetteRef || gazettes[0]?.metadata.name || '';
  f.state.value = dispatch?.spec.state || 'Draft';
  f.cover.value = dispatch?.spec.cover || '';

  const frame = h('iframe', { class: 'preview', sandbox: '', title: 'Preview' });
  const message = h('div');
  const status = h('div', { class: 'status' }, dispatch ? conditions(dispatch) : null);
  const phase = h('span', { class: 'phase' }, dispatch?.status?.phase || 'New');

  const build = (withVersion) => {
    const newLabels = {};
    if (f.section.value.trim()) newLabels[SECTION_LABEL] = f.section.value.trim();
    for (const s of f.stamps.value.split(',').map((x) => x.trim().toLowerCase()).filter(Boolean)) newLabels[STAMP_PREFIX + s] = '';
    const spec = {
      gazetteRef: f.gazette.value,
      title: f.title.value,
      state: f.state.value,
      content: { format: 'markdown', body: f.body.value },
    };
    if (f.summary.value) spec.summary = f.summary.value;
    if (f.cover.value) spec.cover = f.cover.value;
    if (f.publishAt.value) spec.publishAt = new Date(f.publishAt.value).toISOString().replace(/\.\d{3}Z$/, 'Z');
    const metadata = { name: dispatch?.metadata.name || f.name.value, namespace: ns, labels: newLabels };
    if (withVersion && dispatch) {
      // press writing status changes resourceVersion but not generation, and
      // its events can arrive before our own save's response. When the
      // newest object seen has the generation we last saved, nobody else
      // changed the spec: send its version. Otherwise send ours, so the API
      // server refuses to overwrite someone else's edit (409).
      const fresh = latest && latest.metadata.generation === dispatch.metadata.generation;
      metadata.resourceVersion = (fresh ? latest : dispatch).metadata.resourceVersion;
    }
    return { kind: 'Dispatch', metadata, spec };
  };

  let timer;
  const refreshPreview = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      try {
        frame.srcdoc = await api.preview(build(false));
      } catch (err) {
        frame.srcdoc = '';
        message.replaceChildren(failure(err));
      }
    }, 400);
  };
  for (const el of Object.values(f)) el.addEventListener('input', refreshPreview);

  const save = async (force = false) => {
    message.replaceChildren();
    try {
      const saved = await api.apply(ns, 'dispatches', build(!force), { force });
      dispatch = saved;
      message.replaceChildren(notice('Saved.', 'ok'));
      if (!name) location.hash = `#/ns/${encodeURIComponent(ns)}/dispatches/${encodeURIComponent(saved.metadata.name)}`;
    } catch (err) {
      if (err.status === 409) {
        message.replaceChildren(
          notice('Someone else changed this dispatch since you opened it.'),
          h('div', { class: 'toolbar' },
            h('button', { type: 'button', onclick: () => editorView(ns, name) }, 'Load their version (discard mine)'),
            h('button', { type: 'button', class: 'danger', onclick: () => save(true) }, 'Overwrite with mine'),
          ),
        );
      } else {
        message.replaceChildren(failure(err));
      }
    }
  };

  const del = async () => {
    if (!confirm(`Delete “${f.title.value}”? This cannot be undone.`)) return;
    try {
      await api.remove(ns, 'dispatches', name);
      location.hash = `#/ns/${encodeURIComponent(ns)}/dispatches`;
    } catch (err) {
      message.replaceChildren(failure(err));
    }
  };

  const field = (label, el, hint) => h('label', {}, h('span', {}, label), el, hint ? h('small', {}, hint) : null);
  show(
    h('div', { class: 'toolbar' },
      h('h1', {}, dispatch ? dispatch.spec.title : 'New dispatch'), phase,
      h('button', { type: 'button', class: 'primary', onclick: () => save() }, 'Save'),
      dispatch ? h('button', { type: 'button', class: 'danger', onclick: del }, 'Delete') : null,
    ),
    message,
    status,
    h('div', { class: 'editor' },
      h('form', { class: 'fields', onsubmit: (e) => { e.preventDefault(); save(); } },
        dispatch ? null : field('Name (URL slug)', f.name, 'Lowercase letters, digits and dashes.'),
        field('Title', f.title),
        field('Summary', f.summary),
        field('Gazette', f.gazette),
        h('div', { class: 'row' }, field('State', f.state), field('Publish at', f.publishAt, 'Empty publishes immediately.')),
        h('div', { class: 'row' }, field('Section', f.section), field('Stamps', f.stamps, 'Comma-separated.')),
        field('Cover', f.cover),
        field('Body (Markdown)', f.body, 'Reference media as ![alt](print:name).'),
      ),
      frame,
    ),
  );
  refreshPreview();

  if (dispatch) {
    // Follow press's verdict live: phase and conditions.
    api.watch(ns, 'dispatches', ({ object }) => {
      if (object.metadata.name !== dispatch.metadata.name) return;
      phase.textContent = object.status?.phase || '…';
      status.replaceChildren(conditions(object));
      latest = object;
    }, current.signal, { name: dispatch.metadata.name }).catch(() => {});
  }
}

// --- media -----------------------------------------------------------------------

async function mediaView(ns) {
  const signal = show(h('p', {}, 'Loading…'));
  let prints = [];
  let archives = [];
  try {
    const [p, a, ca] = await Promise.all([api.list(ns, 'prints'), api.list(ns, 'archives').catch(() => []), api.listCluster('clusterarchives').catch(() => [])]);
    prints = p;
    archives = [...a.map((x) => ({ kind: 'Archive', name: x.metadata.name })), ...ca.map((x) => ({ kind: 'ClusterArchive', name: x.metadata.name }))];
  } catch (err) {
    show(failure(err));
    return;
  }
  if (signal.aborted) return;

  const file = h('input', { id: 'm-file', type: 'file' });
  const alt = h('input', { id: 'm-alt', placeholder: 'Describe the image' });
  const archive = h('select', { id: 'm-archive' }, archives.map((a, i) => h('option', { value: String(i) }, `${a.name} (${a.kind})`)));
  const progress = h('div', { role: 'status' });
  const grid = h('ul', { class: 'prints' });

  const renderPrints = () =>
    grid.replaceChildren(
      ...prints.map((p) =>
        h('li', {},
          p.spec.contentType.startsWith('image/') && p.status?.phase === 'Ready' ? h('img', { src: p.status.url, alt: p.spec.alt || '' }) : h('div', { class: 'file' }, p.spec.contentType),
          h('code', {}, `print:${p.metadata.name}`),
          h('span', { class: `phase ${(p.status?.phase || '').toLowerCase()}` }, p.status?.phase || '…'),
        ),
      ),
    );

  const send = async (e) => {
    e.preventDefault();
    const chosen = file.files[0];
    if (!chosen || !archives.length) return;
    const ref = archives[Number(archive.value)];
    try {
      const p = await upload(ns, chosen, { name: printName(chosen.name), alt: alt.value, archiveRef: ref }, (step) => progress.replaceChildren(notice(`${step}…`, 'info')));
      progress.replaceChildren(notice(`Uploaded. Use it as ![${p.spec.alt || ''}](print:${p.metadata.name})`, 'ok'));
      prints = await api.list(ns, 'prints');
      renderPrints();
    } catch (err) {
      progress.replaceChildren(failure(err));
    }
  };

  show(
    h('h1', {}, 'Media'),
    archives.length
      ? h('form', { class: 'upload', onsubmit: send }, file, alt, archive, h('button', { type: 'submit', class: 'primary' }, 'Upload'))
      : notice('No Archive is set up for this namespace; ask a publisher to create one.', 'info'),
    progress,
    grid,
  );
  renderPrints();
}

// --- start -------------------------------------------------------------------------

async function start() {
  let me;
  try {
    me = await api.session();
  } catch {
    document.body.replaceChildren(
      h('main', { class: 'signin' }, h('h1', {}, 'Manifesto desk'), h('a', { class: 'button primary', href: '/auth/login' }, 'Sign in')),
    );
    return;
  }
  document.getElementById('who').textContent = me.name || me.username;
  document.getElementById('who').title = me.username;
  document.getElementById('signout').addEventListener('click', async () => {
    await api.logout().catch(() => {});
    location.href = '/';
  });
  document.getElementById('ns-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const ns = document.getElementById('ns').value.trim();
    if (ns) location.hash = `#/ns/${encodeURIComponent(ns)}/dispatches`;
  });
  const list = document.getElementById('ns-recent');
  list.replaceChildren(...recentNamespaces().map((n) => h('option', { value: n })));
  addEventListener('hashchange', route);
  route();
}

start();
