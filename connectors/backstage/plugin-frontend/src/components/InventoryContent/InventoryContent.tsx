// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React from 'react';
import {
  Content,
  ContentHeader,
  Progress,
  ResponseErrorPanel,
  StructuredMetadataTable,
  SupportButton,
  Table,
  TableColumn,
} from '@backstage/core-components';
import { useApi } from '@backstage/core-plugin-api';

import { olivaresApiRef } from '../../api/OlivaresApi';
import { useAsyncData } from '../../hooks/useAsyncData';
import { ToneStatus } from '../common/ToneStatus';
import { kindLabel } from '../../api/transform';
import type { CatalogEntry, InventorySummary } from '../../api/types';

/** Estate counts by kind, rendered as a compact key/value strip. */
const SummaryStrip = ({ summary }: { summary: InventorySummary }) => {
  const rows: Record<string, string> = {};
  for (const kind of Object.keys(summary.by_kind).sort()) {
    const c = summary.by_kind[kind];
    rows[kindLabel(kind)] =
      c.stale > 0 ? `${c.total}  (${c.active} active · ${c.stale} stale)` : `${c.total}`;
  }
  rows.Total = summary.truncated ? `${summary.total}+ (truncated)` : `${summary.total}`;
  return <StructuredMetadataTable metadata={rows} />;
};

const prettyKind = (kind: string) => kind.replace(/_/g, ' ');

const columns: TableColumn<CatalogEntry>[] = [
  { title: 'Kind', field: 'kind', render: row => prettyKind(row.kind), width: '12%' },
  {
    title: 'Name',
    field: 'name',
    render: row => row.ref || row.name,
    highlight: true,
  },
  {
    title: 'Status',
    field: 'status',
    render: row => (
      <ToneStatus tone={row.status === 'stale' ? 'warning' : 'ok'}>
        {row.status}
      </ToneStatus>
    ),
    width: '12%',
  },
  {
    title: 'Signals',
    field: 'signal_sources',
    render: row => (row.signal_sources || []).join(', ') || '—',
  },
  { title: 'Last seen', field: 'last_seen', type: 'datetime' },
  { title: 'Seen', field: 'occurrence_count', type: 'numeric', width: '8%' },
];

/**
 * InventoryContent renders the discovery estate (module I): a counts-by-kind strip
 * over GET /inventory/summary and a searchable table of catalog entities over
 * GET /inventory/entities. It adds no logic — it fetches the engine's catalog and
 * renders it (the same posture as the first-party web app, ARCHITECTURE.md).
 */
export const InventoryContent = () => {
  const api = useApi(olivaresApiRef);
  const summary = useAsyncData(() => api.inventorySummary(), []);
  const entities = useAsyncData(() => api.inventoryEntities({ limit: 200 }), []);

  const error = summary.error || entities.error;
  if (error) {
    return (
      <Content>
        <ResponseErrorPanel error={error} />
      </Content>
    );
  }

  return (
    <Content>
      <ContentHeader title="Inventory">
        <SupportButton>
          The agents, MCP servers, tools and sessions the Olivares control plane has
          discovered across your estate. Read-only.
        </SupportButton>
      </ContentHeader>

      {summary.loading || !summary.value ? (
        <Progress />
      ) : (
        <SummaryStrip summary={summary.value} />
      )}

      {entities.loading && !entities.value ? (
        <Progress />
      ) : (
        <Table<CatalogEntry>
          title={`Catalog entities${
            entities.value?.has_more ? ' (first page)' : ''
          }`}
          options={{ search: true, paging: true, pageSize: 20, padding: 'dense' }}
          columns={columns}
          data={entities.value?.items ?? []}
        />
      )}
    </Content>
  );
};
