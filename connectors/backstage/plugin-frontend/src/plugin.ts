// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import {
  createApiFactory,
  createComponentExtension,
  createPlugin,
  createRoutableExtension,
  discoveryApiRef,
  fetchApiRef,
} from '@backstage/core-plugin-api';

import { OlivaresApiClient, olivaresApiRef } from './api/OlivaresApi';
import { rootRouteRef } from './routes';

/**
 * olivaresPlugin registers the Olivares utility API (bound to Backstage discovery
 * + fetch, so reads carry the signed-in user's identity to the portal proxy) and
 * provides the page + entity extensions below. Install it in the customer's
 * Backstage app (see README) alongside the `olivares-backend` plugin.
 */
export const olivaresPlugin = createPlugin({
  id: 'olivares',
  apis: [
    createApiFactory({
      api: olivaresApiRef,
      deps: { discoveryApi: discoveryApiRef, fetchApi: fetchApiRef },
      factory: ({ discoveryApi, fetchApi }) =>
        new OlivaresApiClient({ discoveryApi, fetchApi }),
    }),
  ],
  routes: {
    root: rootRouteRef,
  },
});

/** The standalone, routable Olivares page (Inventory / Sessions / Access map). */
export const OlivaresPage = olivaresPlugin.provide(
  createRoutableExtension({
    name: 'OlivaresPage',
    component: () => import('./components/OlivaresPage').then(m => m.OlivaresPage),
    mountPoint: rootRouteRef,
  }),
);

/** An entity-page tab: an agent's live sessions + R/RW access, in the catalog flow. */
export const EntityOlivaresContent = olivaresPlugin.provide(
  createRoutableExtension({
    name: 'EntityOlivaresContent',
    component: () =>
      import('./components/entity/EntityOlivaresContent').then(
        m => m.EntityOlivaresContent,
      ),
    mountPoint: rootRouteRef,
  }),
);

/** A compact entity overview card: the agent's governance headline. */
export const EntityOlivaresCard = olivaresPlugin.provide(
  createComponentExtension({
    name: 'EntityOlivaresCard',
    component: {
      lazy: () =>
        import('./components/entity/EntityOlivaresCard').then(
          m => m.EntityOlivaresCard,
        ),
    },
  }),
);
