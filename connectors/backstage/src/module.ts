// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import {
  coreServices,
  createBackendModule,
} from '@backstage/backend-plugin-api';
import { catalogProcessingExtensionPoint } from '@backstage/plugin-catalog-node/alpha';
import { OlivaresEntityProvider } from './OlivaresEntityProvider';

/**
 * catalogModuleOlivares is the new-backend-system module that registers the
 * OlivaresEntityProvider with the catalog. Install it in the
 * customer's Backstage backend alongside the catalog plugin (see README).
 *
 * It is read-first: it only ADDS an entity provider that reads the Olivares
 * inventory and supplies catalog entities. It registers no routes, opens no
 * listener, and mutates neither Backstage config nor the Olivares estate.
 *
 * Wiring (verified against backstage.io external-integrations): obtain the
 * extension point catalogProcessingExtensionPoint and call addEntityProvider.
 * The provider is given a SchedulerServiceTaskRunner so the catalog drives the
 * refresh cadence — the provider never owns a timer (the same scheduling
 * contract as a Go SourceConnector, sdk/connector.go).
 *
 * Config is read from app-config under `olivares.*` (baseUrl/token/tenant) and
 * an optional `olivares.schedule`. The token is read via config (an env
 * placeholder, ${OLIVARES_TOKEN}) and is never inlined.
 */
export const catalogModuleOlivares = createBackendModule({
  pluginId: 'catalog',
  moduleId: 'olivares-entity-provider',
  register(env) {
    env.registerInit({
      deps: {
        catalog: catalogProcessingExtensionPoint,
        config: coreServices.rootConfig,
        scheduler: coreServices.scheduler,
        logger: coreServices.logger,
      },
      async init({ catalog, config, scheduler, logger }) {
        const root = config.getOptionalConfig('olivares');
        const baseUrl = root?.getString('baseUrl');
        if (!root || !baseUrl) {
          // Fail-soft: a backend without `olivares.baseUrl` simply does not run
          // the provider, rather than crashing the whole catalog backend.
          logger.warn(
            'olivares: no `olivares.baseUrl` configured; entity provider not registered',
          );
          return;
        }

        // The catalog drives cadence via a SchedulerServiceTaskRunner. Defaults
        // (30 min refresh, 30 s timeout) are overridable under olivares.schedule.
        const runner = scheduler.createScheduledTaskRunner({
          frequency: {
            minutes: root.getOptionalNumber('schedule.frequencyMinutes') ?? 30,
          },
          timeout: {
            seconds: root.getOptionalNumber('schedule.timeoutSeconds') ?? 30,
          },
        });

        const provider = new OlivaresEntityProvider({
          baseUrl,
          token: root.getOptionalString('token'),
          tenant: root.getOptionalString('tenant'),
          // SchedulerServiceTaskRunner.run({ id, fn }) matches the provider's
          // schedule hook shape.
          schedule: runner,
        });

        catalog.addEntityProvider(provider);
        logger.info(
          `olivares: registered entity provider ${provider.getProviderName()} against ${baseUrl}`,
        );
      },
    });
  },
});
