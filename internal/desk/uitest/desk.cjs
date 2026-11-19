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

    step('open the publication');
    await page.fill('#ns', 'blog-ui');
    await page.press('#ns', 'Enter');
    await page.getByText('No dispatches yet.').waitFor();

    step('write a dispatch with a live preview');
    await page.getByRole('link', { name: 'New dispatch' }).click();
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
