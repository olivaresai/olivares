// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Public surface of the Olivares frontend plugin. The plugin + extensions
// are what a Backstage app wires; the apiRef and `isOlivaresEntity` predicate
// support custom layouts and EntitySwitch conditions; the client/types support
// programmatic use and unit testing.

export {
  olivaresPlugin,
  OlivaresPage,
  EntityOlivaresContent,
  EntityOlivaresCard,
} from './plugin';

export { olivaresApiRef } from './api/OlivaresApi';
export type { OlivaresApi } from './api/OlivaresApi';

export {
  createOlivaresClient,
  OlivaresApiError,
} from './api/client';
export type {
  OlivaresClient,
  FetchLike,
  EntityListParams,
  LiveListParams,
  GraphParams,
  NeighborDirection,
} from './api/client';

export {
  isOlivaresEntity,
  olivaresExternalId,
  matchAgentToEntity,
  filterSessionsForAgent,
  ANNOTATION_MANAGED,
  ANNOTATION_EXTERNAL_ID,
} from './components/entity/olivaresEntity';

export type {
  AgentDTO,
  CatalogEntry,
  InventorySummary,
  LiveDTO,
  TimelineDTO,
  AccessEdge,
  AccessNode,
  GraphResponse,
  DiffResponse,
  DriftEntry,
  Whoami,
} from './api/types';
