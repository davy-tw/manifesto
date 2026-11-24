// Drives desk's UI in Chromium. Run by TestUI: node desk.cjs <desk URL> <screenshot dir>
const { chromium } = require('playwright');
const path = require('path');

(async () => {
  const [url, shots] = process.argv.slice(2);
  const browser = await chromium.launch();
  // The fake OIDC issuer has a self-signed certificate.
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1400, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.log('pageerror:', e.message));
  page.on('console', (m) => { if (m.type() === 'error') console.log('console error:', m.text()); });
  const step = (s) => console.log('step:', s);
  try {
    step('sign in');
    await page.goto(url);
    await page.getByRole('link', { name: 'Sign in' }).click();
    await page.locator('#who', { hasText: 'Alice' }).waitFor();
    await page.getByRole('heading', { name: 'Choose a publication' }).waitFor();
    // First sign-in: no recent namespaces, and no stray "null" either.
    if ((await page.textContent('#main')).includes('null')) throw new Error('namespace chooser shows "null"');
    // Alice may not list Gazettes in every namespace: desk asks for one.
    await page.getByText('Enter the namespace of the Gazette you write for.').waitFor();
    if (await page.locator('#main .notice').count()) throw new Error('a refused cluster-wide list shows an error');
    const refusal = await page.evaluate(() => sessionStorage.getItem('manifesto.gazetteListRefused'));
    if (!refusal) throw new Error('the refusal was not remembered');
    await page.screenshot({ path: path.join(shots, 'desk-home-namespace.png') });

    step('open the publication');
    await page.fill('#home-ns', 'blog-ui');
    await page.press('#home-ns', 'Enter');
    await page.getByText('No dispatches yet.').waitFor();

    step('open it again from the namespace box in the header');
    // The refusal is remembered: the home page does not ask again.
    let asked = false;
    const onRequest = (req) => { if (req.url().endsWith('/v1alpha1/gazettes')) asked = true; };
    page.on('request', onRequest);
    await page.goto(url + '/#/');
    await page.getByRole('heading', { name: 'Choose a publication' }).waitFor();
    await page.getByText('Enter the namespace of the Gazette you write for.').waitFor();
    await page.locator('.recent a', { hasText: 'blog-ui' }).waitFor();
    page.off('request', onRequest);
    if (asked) throw new Error('the refused Gazette list was requested again');
    await page.fill('#ns', 'blog-ui');
    await page.press('#ns', 'Enter');
    await page.getByText('No dispatches yet.').waitFor();

    step('write a dispatch with a live preview');
    await page.getByRole('link', { name: 'New dispatch' }).click();
    await page.locator('#f-name').waitFor();
    if ((await page.textContent('#main')).includes('null')) throw new Error('new dispatch editor shows "null"');
    await page.fill('#f-name', 'hello-ui');
    await page.fill('#f-title', 'Hello from the UI');
    await page.fill('#f-body', '# Written in desk\n\nThe preview uses the real theme.');
    const preview = page.frameLocator('iframe.preview');
    await preview.locator('h1', { hasText: 'Hello from the UI' }).waitFor();
    await preview.getByText("Alice's Notes").first().waitFor();
    await page.getByRole('button', { name: 'Save' }).click();
    await page.waitForURL(/dispatches\/hello-ui$/);
    await page.locator('.toolbar .phase', { hasText: 'Draft' }).waitFor();
    await page.locator('.conditions', { hasText: 'Rendered' }).waitFor();

    step('publish');
    await page.selectOption('#f-state', 'Published');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.locator('.toolbar .phase', { hasText: 'Published' }).waitFor();

    step('save again after press updated the status (no false conflict)');
    await page.fill('#f-summary', 'A summary');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Saved.').waitFor();

    step('resolve a conflicting edit made elsewhere');
    await page.evaluate(async () => {
      const r = await fetch('/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/blog-ui/dispatches/hello-ui?fieldManager=someone-else', {
        method: 'PATCH',
        headers: { 'X-Manifesto-Desk': '1', 'Content-Type': 'application/merge-patch+json' },
        body: JSON.stringify({ spec: { summary: 'Changed elsewhere' } }),
      });
      if (!r.ok) throw new Error(`patch failed: ${r.status}`);
    });
    await page.fill('#f-title', 'Hello again');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Someone else changed this dispatch').waitFor();
    await page.screenshot({ path: path.join(shots, 'desk-conflict.png') });
    await page.getByRole('button', { name: 'Overwrite with mine' }).click();
    await page.getByText('Saved.').waitFor();

    step('see it in the list');
    await page.getByRole('link', { name: 'Dispatches' }).click();
    await page.getByRole('link', { name: 'Hello again' }).waitFor();
    await page.locator('tr', { hasText: 'Hello again' }).locator('.phase', { hasText: 'Published' }).waitFor();
    await page.screenshot({ path: path.join(shots, 'desk-list.png') });

    step('upload media');
    await page.getByRole('link', { name: 'Media' }).click();
    await page.setInputFiles('#m-file', {
      name: 'Cover Image.svg',
      mimeType: 'image/svg+xml',
      buffer: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><rect width="40" height="20" fill="#1d4ed8"/></svg>'),
    });
    await page.fill('#m-alt', 'A cover');
    await page.getByRole('button', { name: 'Upload' }).click();
    await page.getByText('Uploaded.').waitFor({ timeout: 30000 });
    await page.locator('.prints li', { hasText: 'print:cover-image' }).locator('.phase', { hasText: 'Ready' }).waitFor();
    await page.screenshot({ path: path.join(shots, 'desk-media.png') });

    step('use it as the cover');
    await page.getByRole('link', { name: 'Dispatches' }).click();
    await page.getByRole('link', { name: 'Hello again' }).click();
    await page.selectOption('#f-cover', 'print:cover-image');
    await preview.locator('img.cover').waitFor();
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Saved.').waitFor();
    await page.screenshot({ path: path.join(shots, 'desk-editor.png') });

    step('restore the first edition from the history');
    await page.getByText('History').click();
    await page.getByRole('button', { name: 'Load edition 1' }).waitFor();
    await page.screenshot({ path: path.join(shots, 'desk-history.png') });
    await page.getByRole('button', { name: 'Load edition 1' }).click();
    await page.getByText('Loaded edition 1 into the editor').waitFor();
    if ((await page.inputValue('#f-title')) !== 'Hello from the UI') throw new Error('edition 1 did not load');
    await preview.locator('h1', { hasText: 'Hello from the UI' }).waitFor();
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Saved.').waitFor();
    await page.locator('.editions li', { hasText: 'Hello from the UI' }).nth(1).waitFor();

    step('edit after mfctl restored an edition (no false conflict)');
    // mfctl edition restore writes a merge patch as manifesto-mfctl, which
    // then owns those fields; desk must still save over them.
    await page.evaluate(async () => {
      const r = await fetch('/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/blog-ui/dispatches/hello-ui?fieldManager=manifesto-mfctl', {
        method: 'PATCH',
        headers: { 'X-Manifesto-Desk': '1', 'Content-Type': 'application/merge-patch+json' },
        body: JSON.stringify({ spec: { title: 'Restored by mfctl', summary: null } }),
      });
      if (!r.ok) throw new Error(`restore failed: ${r.status}`);
    });
    await page.reload();
    await page.waitForFunction(() => document.querySelector('#f-title')?.value === 'Restored by mfctl');
    await page.fill('#f-title', 'Edited after restore');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Saved.').waitFor();

    step('sign in with a pasted token instead');
    const other = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const tp = await other.newPage();
    tp.setDefaultTimeout(15000);
    await tp.goto(url);
    await tp.fill('#token', 'not-a-token');
    await tp.getByRole('button', { name: 'Sign in with token' }).click();
    await tp.getByText('the API server did not accept this token').waitFor();
    await tp.fill('#token', process.env.DESK_UI_TOKEN);
    await tp.screenshot({ path: path.join(shots, 'desk-token-signin.png') });
    // alice's refusal, remembered in this tab, does not apply to another user.
    await tp.evaluate((v) => sessionStorage.setItem('manifesto.gazetteListRefused', v), refusal);
    await tp.getByRole('button', { name: 'Sign in with token' }).click();
    await tp.locator('#who', { hasText: 'system:serviceaccount:blog-ui:token-user' }).waitFor();
    // This user may list Gazettes everywhere: they are grouped by namespace.
    await tp.locator('.gazettes .namespace h2', { hasText: 'blog-ui' }).waitFor();
    await tp.screenshot({ path: path.join(shots, 'desk-home-gazettes.png') });
    await tp.locator('.gazettes .namespace li', { hasText: "Alice's Notes" }).waitFor();
    // Namespaces listed above are not repeated under Recent.
    await tp.evaluate(() => localStorage.setItem('manifesto.recentNamespaces', JSON.stringify(['blog-ui', 'blog-elsewhere'])));
    await tp.reload();
    await tp.locator('.recent a', { hasText: 'blog-elsewhere' }).waitFor();
    if (await tp.locator('.recent a', { hasText: 'blog-ui' }).count()) throw new Error('Recent repeats a listed namespace');
    // Any other failure is shown, and the namespace form stays.
    await tp.route('**/v1alpha1/gazettes', (r) => r.fulfill({ status: 500, contentType: 'text/plain', body: 'etcd is on fire' }));
    await tp.reload();
    await tp.locator('#main .notice', { hasText: 'etcd is on fire' }).waitFor();
    await tp.locator('#home-ns').waitFor();
    await tp.unroute('**/v1alpha1/gazettes');
    await tp.reload();
    await tp.locator('.gazettes .namespace h2 a', { hasText: 'blog-ui' }).click();
    await tp.getByRole('link', { name: 'Edited after restore' }).waitFor();
    await other.close();
    console.log('UI OK');
  } catch (e) {
    const state = await page.evaluate(async () => {
      const r = await fetch('/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/blog-ui/dispatches/hello-ui');
      const d = await r.json();
      return { spec: d.spec && d.spec.state, generation: d.metadata && d.metadata.generation, status: d.status };
    }).catch((err) => String(err));
    console.log('dispatch now:', JSON.stringify(state));
    await page.screenshot({ path: path.join(shots, 'desk-failure.png'), fullPage: true }).catch(() => {});
    console.error(e);
    process.exitCode = 1;
  } finally {
    await browser.close();
  }
})();
