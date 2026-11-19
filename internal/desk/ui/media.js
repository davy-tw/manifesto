// Uploads a file as a Print: hash it in the browser, create the Print, wait
// for press to hand out an upload URL, PUT the file there, wait until Ready.

import * as api from './api.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function hex(buf) {
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

// printName turns a file name into a Print name (a DNS label).
export function printName(fileName) {
  const base = fileName.replace(/\.[^.]*$/, '').toLowerCase();
  const slug = base.replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 63).replace(/-+$/, '');
  return slug || 'file';
}

async function waitFor(ns, name, done, timeoutMs) {
  const until = Date.now() + timeoutMs;
  for (;;) {
    const p = await api.get(ns, 'prints', name);
    const result = done(p);
    if (result) return p;
    if (p.status?.phase === 'Failed') throw new Error(p.status.conditions?.find((c) => c.type === 'Ready')?.message || 'upload failed');
    if (Date.now() > until) throw new Error('timed out waiting for press');
    await sleep(1000);
  }
}

// upload stores file as Print name in archiveRef, reporting progress with
// onStep(text). It returns the Ready Print.
export async function upload(ns, file, { name, alt, archiveRef }, onStep = () => {}) {
  onStep('Hashing');
  const sha256 = hex(await crypto.subtle.digest('SHA-256', await file.arrayBuffer()));
  const contentType = file.type || 'application/octet-stream';
  onStep('Creating the Print');
  await api.apply(ns, 'prints', {
    kind: 'Print',
    metadata: { name, namespace: ns },
    spec: { archiveRef, contentType, size: file.size, sha256, ...(alt ? { alt } : {}) },
  });
  onStep('Waiting for an upload URL');
  // A file already stored in this namespace makes the Print Ready at once.
  const p = await waitFor(ns, name, (p) => p.status?.phase === 'Ready' || p.status?.uploadURL, 60000);
  if (p.status.phase === 'Ready') return p;
  onStep('Uploading');
  const res = await fetch(p.status.uploadURL, { method: 'PUT', body: file, headers: { 'Content-Type': contentType } });
  if (!res.ok) throw new Error(`upload refused (${res.status})`);
  onStep('Verifying');
  return waitFor(ns, name, (p) => p.status?.phase === 'Ready', 120000);
}
