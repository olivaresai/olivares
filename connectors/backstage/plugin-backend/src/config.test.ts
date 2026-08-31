// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ConfigReader, parseOlivaresConfig } from './config';

/**
 * fakeConfig builds a minimal ConfigReader over a plain object, mimicking the
 * Backstage Config contract closely enough for parseOlivaresConfig: getString
 * throws on a missing key, the optional getters return undefined.
 */
function fakeConfig(data: Record<string, unknown> | undefined): ConfigReader {
  const reader = (d: Record<string, unknown> | undefined): ConfigReader => ({
    getOptionalConfig: key => {
      const v = d?.[key];
      return v && typeof v === 'object' ? reader(v as Record<string, unknown>) : undefined;
    },
    getString: key => {
      const v = d?.[key];
      if (typeof v !== 'string') {
        throw new Error(`missing required config key '${key}'`);
      }
      return v;
    },
    getOptionalString: key => {
      const v = d?.[key];
      return typeof v === 'string' ? v : undefined;
    },
    getOptionalNumber: key => {
      const v = d?.[key];
      return typeof v === 'number' ? v : undefined;
    },
    getOptionalBoolean: key => {
      const v = d?.[key];
      return typeof v === 'boolean' ? v : undefined;
    },
  });
  return reader({ olivares: data });
}

test('parseOlivaresConfig reads the full block and normalizes baseUrl', () => {
  const cfg = parseOlivaresConfig(
    fakeConfig({
      baseUrl: 'https://olivares.example.com/',
      token: 'tok',
      tenant: 'acme',
      timeoutSeconds: 10,
    }),
  );
  assert.equal(cfg.baseUrl, 'https://olivares.example.com');
  assert.equal(cfg.token, 'tok');
  assert.equal(cfg.tenant, 'acme');
  assert.equal(cfg.attributeOnBehalfOf, true);
  assert.equal(cfg.timeoutMs, 10_000);
});

test('parseOlivaresConfig defaults attribution on and timeout to 30s', () => {
  const cfg = parseOlivaresConfig(fakeConfig({ baseUrl: 'https://cp' }));
  assert.equal(cfg.attributeOnBehalfOf, true);
  assert.equal(cfg.timeoutMs, 30_000);
  assert.equal(cfg.token, undefined);
  assert.equal(cfg.tenant, undefined);
});

test('parseOlivaresConfig honors attributeOnBehalfOf=false', () => {
  const cfg = parseOlivaresConfig(
    fakeConfig({ baseUrl: 'https://cp', attributeOnBehalfOf: false }),
  );
  assert.equal(cfg.attributeOnBehalfOf, false);
});

test('parseOlivaresConfig throws when the olivares block is missing', () => {
  assert.throws(() => parseOlivaresConfig(fakeConfig(undefined)), /missing `olivares` config/);
});

test('parseOlivaresConfig throws when baseUrl is missing', () => {
  assert.throws(() => parseOlivaresConfig(fakeConfig({ token: 'tok' })), /baseUrl/);
});
