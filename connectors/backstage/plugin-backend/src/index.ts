// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Public surface of the Olivares portal-proxy backend plugin. The default
// export is the new-backend-system plugin a Backstage backend installs; the named
// exports support custom wiring and unit-testing the framework-free logic.
export { olivaresPlugin as default } from './plugin';
export { olivaresPlugin } from './plugin';
export { createRouter } from './service/router';
export type { RouterOptions } from './service/router';
export { parseOlivaresConfig } from './config';
export type { ConfigReader, OlivaresConfig } from './config';
export {
  ROUTES,
  isSafePathParam,
  pickQuery,
  upstreamPathFor,
} from './allowlist';
export type { RouteKey, RouteSpec } from './allowlist';
export {
  buildQueryString,
  buildUpstreamHeaders,
  buildUpstreamUrl,
  joinUrl,
  trimTrailingSlash,
} from './upstream';
export type { UpstreamHeaderInput } from './upstream';
