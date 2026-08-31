// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { HttpAuthService, LoggerService, UserInfoService } from '@backstage/backend-plugin-api';
import express from 'express';
import Router from 'express-promise-router';

import { OlivaresConfig } from '../config';
import { RouteKey, pickQuery, upstreamPathFor } from '../allowlist';
import { buildUpstreamHeaders, buildUpstreamUrl } from '../upstream';

/** RouterOptions are the wired backend services + the parsed control-plane config. */
export interface RouterOptions {
  logger: LoggerService;
  config: OlivaresConfig;
  httpAuth: HttpAuthService;
  userInfo: UserInfoService;
  /** Injectable fetch for tests; defaults to the global fetch. */
  fetchImpl?: typeof fetch;
}

/**
 * createRouter builds the Olivares portal proxy router, mounted by the plugin at
 * /api/olivares. It is READ-ONLY by construction: only the GET routes below are
 * registered, each resolves through the closed allow-list, and any other path or
 * method falls through to 404/405. Two independent guards protect every read:
 *
 *  1. PORTAL AUTHENTICATION — `httpAuth.credentials(req, { allow: ['user'] })`
 *     requires a signed-in Backstage user. An anonymous or service principal is
 *     rejected before any upstream call; this plugin does not add a Backstage
 *     permission/RBAC decision.
 *  2. CONTROL-PLANE RBAC — the read is forwarded with the configured service
 *     token (never exposed to the browser); the control plane still enforces its
 *     own read-tier permissions and self-audits the privileged reads as that
 *     service-token principal. X-Olivares-On-Behalf-Of is only an informational
 *     identity hint; the current control plane does not consume it for ledger
 *     attribution or authorization.
 */
export async function createRouter(options: RouterOptions): Promise<express.Router> {
  const { logger, config, httpAuth, userInfo } = options;
  const doFetch = options.fetchImpl ?? fetch;

  const router = Router();
  router.use(express.json());

  // Liveness: no upstream call, no auth — lets the host confirm the plugin mounted.
  router.get('/health', (_req, res) => {
    res.json({ status: 'ok' });
  });

  // proxy returns an Express handler that authenticates the portal user, forwards
  // the allow-listed read to the control plane, and relays the JSON response.
  const proxy =
    (key: RouteKey) =>
    async (req: express.Request, res: express.Response): Promise<void> => {
      // (1) Portal SSO gate: require a Backstage USER credential.
      const credentials = await httpAuth.credentials(req, { allow: ['user'] });

      // Resolve the caller for honest upstream attribution (best-effort).
      let onBehalfOf: string | undefined;
      if (config.attributeOnBehalfOf) {
        try {
          onBehalfOf = (await userInfo.getUserInfo(credentials)).userEntityRef;
        } catch {
          // A token without a resolvable user identity still authenticates; we
          // simply forward the read without the attribution header.
          onBehalfOf = undefined;
        }
      }

      const upstreamPath = upstreamPathFor(key, req.params as Record<string, string>);
      const url = buildUpstreamUrl(config.baseUrl, upstreamPath, pickQuery(key, req.query));
      const headers = buildUpstreamHeaders({
        token: config.token,
        tenant: config.tenant,
        onBehalfOf,
      });

      let upstream: Response;
      try {
        upstream = await doFetch(url, {
          method: 'GET',
          headers,
          signal: AbortSignal.timeout(config.timeoutMs),
        });
      } catch (err) {
        // Network failure / timeout: the upstream is unreachable, not the client's
        // fault. Never echo the error detail (it may name internal hosts).
        logger.warn(`olivares proxy: upstream ${key} unreachable: ${String(err)}`);
        res.status(502).json({ error: { message: 'control plane unreachable' } });
        return;
      }

      // The browser must never cache a privileged, per-user read.
      res.setHeader('Cache-Control', 'no-store');

      if (!upstream.ok) {
        // Relay the upstream status (401/403/404/429 are meaningful to the UI) but
        // not the body, which may carry detail we should not forward to the portal.
        const status = upstream.status >= 500 ? 502 : upstream.status;
        res.status(status).json({
          error: { message: `control plane responded ${upstream.status}` },
        });
        return;
      }

      const body = await upstream.json().catch(() => ({}));
      res.json(body);
    };

  // The read-only surface. Every handler is a GET; nothing here mutates.
  router.get('/whoami', proxy('whoami'));
  router.get('/agents', proxy('agents'));
  router.get('/access-edges', proxy('accessEdges'));
  router.get('/inventory/summary', proxy('inventorySummary'));
  router.get('/inventory/entities', proxy('inventoryEntities'));
  router.get('/sessions/live', proxy('sessionsLive'));
  router.get('/sessions/live/:ref', proxy('sessionsLiveOne'));
  router.get('/sessions/live/:ref/timeline', proxy('sessionsTimeline'));
  router.get('/access-map/graph', proxy('accessMapGraph'));
  router.get('/access-map/neighbors', proxy('accessMapNeighbors'));
  router.get('/access-map/drift', proxy('accessMapDrift'));

  // Anything not matched above is either an unknown path or a write attempt; both
  // are refused. A non-GET on a known read path is a 405; everything else is 404.
  router.all('*', (req, res) => {
    if (req.method !== 'GET') {
      res.status(405).json({ error: { message: 'read-only proxy: method not allowed' } });
      return;
    }
    res.status(404).json({ error: { message: 'not found' } });
  });

  return router;
}
