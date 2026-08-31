// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Public surface of the Olivares catalog backend module. The
// default export is the new-backend-system module a Backstage backend installs;
// the named exports support custom wiring and unit testing of the mapper.
export { catalogModuleOlivares as default } from './module';
export { catalogModuleOlivares } from './module';
export {
  OlivaresEntityProvider,
  mapEstateToEntities,
} from './OlivaresEntityProvider';
export type { AgentDTO, IdentityDTO } from './OlivaresEntityProvider';
