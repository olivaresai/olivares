// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// A browser's virtual authenticator drives the real registration/step-up ceremony.
const { chromium } = require('@playwright/test');
const fs = require('node:fs');

// The page may report only these stages; anything else it sends is ignored.
const PAGE_STAGES = new Set(['register-options', 'credential-create', 'register',
  'authenticate-options', 'assertion', 'authenticate']);
const CLOSE_MS = 5000;

(async () => {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));
  // The stage is written before the step that can hang, so it survives the caller killing this process.
  // Fixed names only: browser errors can contain tokens and request payloads.
  const mark = name => fs.writeFileSync(input.stageFile, name);
  mark('launch');
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined,
  });
  try {
    mark('new-page');
    const page = await browser.newPage();
    await page.exposeFunction('passkeyStage', name => { if (PAGE_STAGES.has(name)) mark(name); });
    mark('login');
    await page.goto(input.origin + '/login', { waitUntil: 'domcontentloaded' });
    mark('cdp');
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    await cdp.send('WebAuthn.addVirtualAuthenticator', { options: {
      protocol: 'ctap2', transport: 'usb', hasResidentKey: true,
      hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
    } });
    mark('evaluate');
    const result = await page.evaluate(async ({ token, tenant }) => {
      const headers = { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token,
        'X-Olivares-Tenant': tenant };
      const call = async (path, body = {}) => {
        const response = await fetch('/v1/auth/webauthn/' + path,
          { method: 'POST', headers, body: JSON.stringify(body) });
        if (!response.ok) throw new Error(path + ': HTTP ' + response.status);
        return response.json();
      };
      const decode = value => Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/')),
        c => c.charCodeAt(0));
      const encode = value => btoa(String.fromCharCode(...new Uint8Array(value)))
        .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      const pack = credential => ({ id: credential.id, rawId: encode(credential.rawId),
        type: credential.type, clientExtensionResults: credential.getClientExtensionResults(),
        response: Object.fromEntries(['clientDataJSON', 'attestationObject', 'authenticatorData',
          'signature', 'userHandle'].filter(key => credential.response[key] != null)
          .map(key => [key, encode(credential.response[key])])) });
      await window.passkeyStage('register-options');
      const creation = (await call('register/options')).publicKey;
      creation.challenge = decode(creation.challenge);
      creation.user.id = decode(creation.user.id);
      for (const entry of creation.excludeCredentials || []) entry.id = decode(entry.id);
      await window.passkeyStage('credential-create');
      const created = await navigator.credentials.create({ publicKey: creation });
      await window.passkeyStage('register');
      await call('register', { name: 'Upgrade test key', credential: pack(created) });
      await window.passkeyStage('authenticate-options');
      const assertion = (await call('authenticate/options')).publicKey;
      assertion.challenge = decode(assertion.challenge);
      for (const entry of assertion.allowCredentials || []) entry.id = decode(entry.id);
      await window.passkeyStage('assertion');
      const signed = await navigator.credentials.get({ publicKey: assertion });
      await window.passkeyStage('authenticate');
      return call('authenticate', { credential: pack(signed) });
    }, input);
    if (result.aal !== 3) throw new Error('the verified ceremony did not elevate the session');
    process.stdout.write(JSON.stringify({ aal: result.aal }) + '\n');
    // Only a clean run reaches here: after a failure the stage file keeps the stage that failed.
    mark('browser-close');
  } finally {
    // A close that throws or hangs must not replace the error that is already being reported.
    await Promise.race([browser.close().catch(() => {}), new Promise(done => setTimeout(done, CLOSE_MS))]);
  }
})().catch(() => {
  process.exitCode = 1;
}).finally(() => process.exit());
