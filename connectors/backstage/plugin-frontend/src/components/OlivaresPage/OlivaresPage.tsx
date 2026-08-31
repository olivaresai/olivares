// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React from 'react';
import { Header, HeaderLabel, Page, TabbedLayout } from '@backstage/core-components';

import { InventoryContent } from '../InventoryContent/InventoryContent';
import { SessionsContent } from '../SessionsContent/SessionsContent';
import { AccessMapContent } from '../AccessMapContent/AccessMapContent';

/**
 * OlivaresPage is the standalone, routable plugin page. It is the platform
 * engineer's Olivares home inside Backstage: three tabs — the discovered
 * Inventory, the live Sessions, and the R/RW Access map + drift — each backed by
 * the portal proxy and the control plane's own SSO/RBAC. It is read-only (v1); manage-as-code is a later session.
*/
export const OlivaresPage = () => (
  <Page themeId="tool">
    <Header
      title="Olivares AI"
      subtitle="Agent governance in your portal — inventory, live sessions, R/RW access"
    >
      <HeaderLabel label="Scope" value="Read-only" />
    </Header>
    <TabbedLayout>
      <TabbedLayout.Route path="/" title="Inventory">
        <InventoryContent />
      </TabbedLayout.Route>
      <TabbedLayout.Route path="/sessions" title="Sessions">
        <SessionsContent />
      </TabbedLayout.Route>
      <TabbedLayout.Route path="/access-map" title="Access map">
        <AccessMapContent />
      </TabbedLayout.Route>
    </TabbedLayout>
  </Page>
);
