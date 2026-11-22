// Talks to desk's backend. Every call is made as the signed-in user: desk
// forwards it to the Kubernetes API server with the user's own token.

const GROUP = 'manifesto.davy.tw';
const VERSION = 'v1alpha1';
export const FIELD_MANAGER = 'manifesto-desk';

export class APIError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// refreshing is the one POST /auth/refresh in flight. Every request that
// finds the session expired waits for it instead of refreshing on its own:
// identity providers that rotate refresh tokens end the whole session when
// one refresh token is used twice. A Web Lock does the same across tabs,
// which share the cookie: a tab that refreshes after another one sends the
// renewed cookie, and desk does not refresh it again. Web Locks exist only
// in secure contexts (https, or localhost): over plain http each tab
// refreshes on its own, and tabs on different desk replicas may reuse a
// refresh token.
let refreshing = null;

// refreshSession resolves to the status of POST /auth/refresh (0 when it
// could not be sent).
function refreshSession() {
  const post = () =>
    fetch('/auth/refresh', {
      method: 'POST',
      headers: { 'X-Manifesto-Desk': '1' },
      credentials: 'same-origin',
    }).then((res) => res.status);
  refreshing ??= (navigator.locks ? navigator.locks.request('manifesto-desk-refresh', post) : post())
    .catch(() => 0)
    .finally(() => {
      refreshing = null;
    });
  return refreshing;
}

// send is fetch with the session cookie. When desk says the session
// expired, it renews the session once and sends the request again. A
// renewal that failed for now (the identity provider is down) is an error
// to retry, not the end of the session.
async function send(path, init) {
  const req = { ...init, credentials: 'same-origin' };
  const res = await fetch(path, req);
  if (res.status !== 401 || !res.headers.get('X-Manifesto-Desk-Refresh')) return res;
  const status = await refreshSession();
  if (status === 401) return res;
  if (status < 200 || status >= 300) {
    throw new APIError(503, 'Could not renew your session right now; try again in a moment.');
  }
  return fetch(path, req);
}

async function call(method, path, { body, contentType } = {}) {
  const headers = { Accept: 'application/json' };
  // desk refuses state changes without this header (CSRF protection).
  if (method !== 'GET') headers['X-Manifesto-Desk'] = '1';
  if (body !== undefined) headers['Content-Type'] = contentType || 'application/json';
  const res = await send(path, {
    method,
    headers,
    body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
  });
  if (!res.ok) {
    let message = await res.text();
    try {
      message = JSON.parse(message).message || message;
    } catch {
      // plain text
    }
    throw new APIError(res.status, message.trim());
  }
  if (res.status === 204) return null;
  const type = res.headers.get('Content-Type') || '';
  return type.includes('json') ? res.json() : res.text();
}

const collection = (ns, plural) =>
  `/k8s/apis/${GROUP}/${VERSION}/namespaces/${encodeURIComponent(ns)}/${plural}`;

export const session = () => call('GET', '/api/session');
export const logout = () => call('POST', '/auth/logout');
export const signInMethods = () => call('GET', '/auth/methods');
export const signInWithToken = (token) => call('POST', '/auth/token', { body: { token } });
export const list = (ns, plural) => call('GET', collection(ns, plural)).then((l) => l.items);
export const listSelected = (ns, plural, labelSelector) =>
  call('GET', `${collection(ns, plural)}?labelSelector=${encodeURIComponent(labelSelector)}`).then((l) => l.items);
export const listCluster = (plural) =>
  call('GET', `/k8s/apis/${GROUP}/${VERSION}/${plural}`).then((l) => l.items);
export const get = (ns, plural, name) => call('GET', `${collection(ns, plural)}/${encodeURIComponent(name)}`);
export const remove = (ns, plural, name) =>
  call('DELETE', `${collection(ns, plural)}/${encodeURIComponent(name)}`);
export const preview = (dispatch) => call('POST', '/api/preview', { body: { dispatch } });

// apply writes obj with server-side apply as manifesto-desk. It always
// forces field ownership: fields last written by someone else (mfctl edition
// restore, kubectl edit) must not block an author's save. Concurrent edits
// are caught by the resourceVersion instead: when obj.metadata.resourceVersion
// is set, the API server refuses the write with 409 if the object changed
// since it was read. Leave it out only to overwrite on purpose.
export function apply(ns, plural, obj) {
  return call('PATCH', `${collection(ns, plural)}/${encodeURIComponent(obj.metadata.name)}?fieldManager=${FIELD_MANAGER}&force=true`, {
    body: { apiVersion: `${GROUP}/${VERSION}`, ...obj },
    contentType: 'application/apply-patch+yaml',
  });
}

// watch calls onEvent({type, object}) for every change in a collection until
// signal aborts, reconnecting when the server ends the stream.
export async function watch(ns, plural, onEvent, signal, { name } = {}) {
  let rv = '';
  while (!signal.aborted) {
    // No allowWatchBookmarks: with it the API server was seen to stop
    // sending events after the first bookmark. Reconnecting from the last
    // resourceVersion seen does not need bookmarks.
    const q = new URLSearchParams({ watch: 'true' });
    if (rv) q.set('resourceVersion', rv);
    if (name) q.set('fieldSelector', `metadata.name=${name}`);
    try {
      const res = await send(`${collection(ns, plural)}?${q}`, { signal });
      if (!res.ok) throw new APIError(res.status, await res.text());
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let nl;
        while ((nl = buf.indexOf('\n')) >= 0) {
          const line = buf.slice(0, nl);
          buf = buf.slice(nl + 1);
          if (!line.trim()) continue;
          const ev = JSON.parse(line);
          if (ev.type === 'ERROR') {
            rv = ''; // expired resourceVersion: start over
            continue;
          }
          rv = ev.object?.metadata?.resourceVersion || rv;
          onEvent(ev);
        }
      }
    } catch (err) {
      if (signal.aborted) return;
      if (err instanceof APIError && (err.status === 401 || err.status === 403)) throw err;
      await new Promise((r) => setTimeout(r, 2000));
    }
  }
}
