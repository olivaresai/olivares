// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Parsing of the `olivares.*` app-config block into a validated, framework-free
// shape. It reads the SAME config block the existing catalog entity provider uses
// (olivares.baseUrl / token / tenant), so an operator configures the control-plane
// connection once and both the catalog provider and this portal proxy consume it.

import { trimTrailingSlash } from './upstream';

/**
 * ConfigReader is the minimal subset of Backstage's `Config` this module needs.
 * Declaring it structurally (rather than importing @backstage/config) keeps this
 * file framework-free and unit-testable with a plain object, while the real
 * rootConfig satisfies it at the call site.
 */
export interface ConfigReader {
  getOptionalConfig(key: string): ConfigReader | undefined;
  getString(key: string): string;
  getOptionalString(key: string): string | undefined;
  getOptionalNumber(key: string): number | undefined;
  getOptionalBoolean(key: string): boolean | undefined;
}

/** The validated control-plane connection settings. */
export interface OlivaresConfig {
  /** The control-plane REST base, normalized without a trailing slash. */
  baseUrl: string;
  /** Bearer token for the control plane (from an env placeholder; never inlined). */
  token?: string;
  /** Optional tenant selector for multi-tenant control planes. */
  tenant?: string;
  /**
   * Whether to forward the calling portal user's entity ref as the informational
   * X-Olivares-On-Behalf-Of header. The current control plane does not use it for
   * authorization or ledger attribution. Default true; set false for deployments
   * whose control plane rejects unknown headers.
   */
  attributeOnBehalfOf: boolean;
  /** Upstream request timeout in milliseconds (from olivares.timeoutSeconds). */
  timeoutMs: number;
}

/** Default upstream timeout when olivares.timeoutSeconds is unset. */
export const DEFAULT_TIMEOUT_SECONDS = 30;

/**
 * parseOlivaresConfig reads and validates the `olivares` block. It throws a
 * descriptive error when the block or its required `baseUrl` is missing so a
 * misconfigured backend fails loudly at startup rather than silently proxying
 * nowhere. The token is intentionally optional here: the control plane may be
 * reachable with a deployment-level credential, and a missing token surfaces as
 * an upstream 401 the UI renders, not a backend crash.
 */
export function parseOlivaresConfig(root: ConfigReader): OlivaresConfig {
  const o = root.getOptionalConfig('olivares');
  if (!o) {
    throw new Error(
      'olivares: missing `olivares` config block (set olivares.baseUrl and olivares.token)',
    );
  }
  // getString throws if baseUrl is absent — exactly the loud failure we want.
  const baseUrl = trimTrailingSlash(o.getString('baseUrl'));
  return {
    baseUrl,
    token: o.getOptionalString('token'),
    tenant: o.getOptionalString('tenant'),
    attributeOnBehalfOf: o.getOptionalBoolean('attributeOnBehalfOf') ?? true,
    timeoutMs:
      (o.getOptionalNumber('timeoutSeconds') ?? DEFAULT_TIMEOUT_SECONDS) * 1000,
  };
}
