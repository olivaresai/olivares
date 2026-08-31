// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { createRouteRef } from '@backstage/core-plugin-api';

/** The plugin's root route — the mount point for the standalone Olivares page. */
export const rootRouteRef = createRouteRef({
  id: 'olivares',
});
