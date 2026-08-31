// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import {
  DiscoveryApi,
  FetchApi,
  createApiRef,
} from '@backstage/core-plugin-api';

import {
  EntityListParams,
  GraphParams,
  LiveListParams,
  NeighborDirection,
  OlivaresClient,
  createOlivaresClient,
} from './client';

/**
 * OlivaresApi is the Backstage utility API the components consume. Its surface is
 * exactly the framework-free OlivaresClient — the React layer never talks to the
 * proxy directly, it goes through this ref so it can be mocked in tests and so the
 * base URL + auth come from Backstage's own discovery + fetch services.
 */
export type OlivaresApi = OlivaresClient;

/** The utility-API ref the plugin registers and components resolve via useApi. */
export const olivaresApiRef = createApiRef<OlivaresApi>({
  id: 'plugin.olivares.service',
});

/**
 * OlivaresApiClient binds the framework-free client to Backstage. It resolves the
 * proxy base URL once via the DiscoveryApi (`/api/olivares`) and issues every
 * request through the FetchApi, whose `fetch` automatically attaches the signed-in
 * user's Backstage identity token — so the backend's user-credential gate (and
 * thus the portal's SSO) is satisfied without the components handling any token.
 */
export class OlivaresApiClient implements OlivaresApi {
  private cached?: Promise<OlivaresClient>;

  constructor(
    private readonly opts: { discoveryApi: DiscoveryApi; fetchApi: FetchApi },
  ) {}

  /** client lazily builds (and memoizes) the bound client at first use. */
  private client(): Promise<OlivaresClient> {
    if (this.cached) {
      return this.cached;
    }
    const { discoveryApi, fetchApi } = this.opts;
    const built = discoveryApi.getBaseUrl('olivares').then(baseUrl =>
      createOlivaresClient({
        baseUrl,
        // Wrap so `this` binding is preserved and the structural FetchLike type
        // is satisfied regardless of the host's exact RequestInit shape.
        fetch: (input, init) => fetchApi.fetch(input, init),
      }),
    );
    this.cached = built;
    return built;
  }

  whoami = async () => (await this.client()).whoami();
  agents = async (params?: { limit?: number; cursor?: string }) =>
    (await this.client()).agents(params);
  inventorySummary = async () => (await this.client()).inventorySummary();
  inventoryEntities = async (params?: EntityListParams) =>
    (await this.client()).inventoryEntities(params);
  sessionsLive = async (params?: LiveListParams) =>
    (await this.client()).sessionsLive(params);
  sessionGet = async (ref: string) => (await this.client()).sessionGet(ref);
  sessionTimeline = async (ref: string, params?: { limit?: number; cursor?: string }) =>
    (await this.client()).sessionTimeline(ref, params);
  accessGraph = async (params?: GraphParams) =>
    (await this.client()).accessGraph(params);
  accessNeighbors = async (id: string, direction?: NeighborDirection, kind?: string) =>
    (await this.client()).accessNeighbors(id, direction, kind);
  accessDrift = async (params?: GraphParams) =>
    (await this.client()).accessDrift(params);
}
