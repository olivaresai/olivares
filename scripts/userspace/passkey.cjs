// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// A browser's virtual authenticator drives the real registration/step-up ceremony.
const { chromium } = require('@playwright/test');
const fs = require('node:fs');
let stage = 'browser-launch';

(async () => {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined,
  });
  try {
    stage = 'browser-setup';
    const page = await browser.newPage();
    await page.goto(input.origin + '/login', { waitUntil: 'domcontentloaded' });
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    await cdp.send('WebAuthn.addVirtualAuthenticator', { options: {
      protocol: 'ctap2', transport: 'usb', hasResidentKey: true,
      hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
    } });
    stage = 'webauthn';
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
      const creation = (await call('register/options')).publicKey;
      creation.challenge = decode(creation.challenge);
      creation.user.id = decode(creation.user.id);
      for (const entry of creation.excludeCredentials || []) entry.id = decode(entry.id);
      await call('register', { name: 'Upgrade test key',
        credential: pack(await navigator.credentials.create({ publicKey: creation })) });
      const assertion = (await call('authenticate/options')).publicKey;
      assertion.challenge = decode(assertion.challenge);
      for (const entry of assertion.allowCredentials || []) entry.id = decode(entry.id);
      return call('authenticate', { credential: pack(await navigator.credentials.get({ publicKey: assertion })) });
    }, input);
    if (result.aal !== 3) throw new Error('the verified ceremony did not elevate the session');
    process.stdout.write(JSON.stringify({ aal: result.aal }) + '\n');
  } finally {
    await browser.close();
  }
})().catch(() => {
  // Fixed stages only: browser errors can contain tokens and request payloads.
  process.stderr.write(`WebAuthn upgrade fixture failed at ${stage}\n`);
  process.exitCode = 1;
});
