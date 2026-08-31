// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import {
  coreServices,
  createBackendPlugin,
} from '@backstage/backend-plugin-api';

import { parseOlivaresConfig } from './config';
import { createRouter } from './service/router';

/**
 * olivaresPlugin is the new-backend-system plugin that mounts the Olivares portal
 * proxy at /api/olivares. Install it in the customer's Backstage backend
 * next to the catalog plugin and the Olivares catalog module (see README):
 *
 *   backend.add(import('@olivaresai/backstage-plugin-olivares-backend'));
 *
 * It registers a read-only HTTP surface only. It opens no upstream connection at
 * startup, owns no timer, and mutates neither Backstage nor the Olivares estate —
 * every route forwards a single GET to a documented control-plane read endpoint
 * (config in the shared `olivares.*` block). If `olivares.baseUrl` is absent it
 * fails loudly at init, the standard posture for a required integration backend.
 */
export const olivaresPlugin = createBackendPlugin({
  pluginId: 'olivares',
  register(env) {
    env.registerInit({
      deps: {
        logger: coreServices.logger,
        config: coreServices.rootConfig,
        httpAuth: coreServices.httpAuth,
        httpRouter: coreServices.httpRouter,
        userInfo: coreServices.userInfo,
      },
      async init({ logger, config, httpAuth, httpRouter, userInfo }) {
        const olivares = parseOlivaresConfig(config);
        httpRouter.use(
          await createRouter({ logger, config: olivares, httpAuth, userInfo }),
        );
        logger.info(`olivares: portal proxy mounted against ${olivares.baseUrl}`);
      },
    });
  },
});
