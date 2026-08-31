// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React from 'react';
import {
  Content,
  ContentHeader,
  InfoCard,
  Progress,
  ResponseErrorPanel,
  SupportButton,
  Table,
  TableColumn,
} from '@backstage/core-components';
import { Grid } from '@material-ui/core';
import { useApi } from '@backstage/core-plugin-api';

import { olivaresApiRef } from '../../api/OlivaresApi';
import { useAsyncData } from '../../hooks/useAsyncData';
import { ToneStatus } from '../common/ToneStatus';
import {
  attributionMeta,
  driftEntryLabel,
  driftEntryTone,
  isWriteMode,
  modeToken,
  summarizeDrift,
  summarizeGraph,
  type Tone,
} from '../../api/transform';
import type {
  AccessEdge,
  DiffResponse,
  DriftEntry,
  GraphResponse,
} from '../../api/types';

const originLabel = (e: AccessEdge) => e.origin_ref || `${e.origin_kind}:${e.origin_id}`;
const resourceLabel = (e: AccessEdge) =>
  e.resource_ref || `${e.resource_kind || 'resource'}:${e.resource_id}`;

/** A mode cell that emphasizes the risk-bearing write/readwrite edges. */
const ModeCell = ({ mode }: { mode: string }) => (
  <ToneStatus tone={isWriteMode(mode) ? 'warning' : 'default'}>{modeToken(mode)}</ToneStatus>
);

const edgeColumns: TableColumn<AccessEdge>[] = [
  { title: 'Origin', field: 'origin_ref', render: originLabel, highlight: true },
  { title: 'Mode', field: 'mode', render: row => <ModeCell mode={row.mode} />, width: '8%' },
  { title: 'Resource', field: 'resource_ref', render: resourceLabel },
  {
    title: 'Obs / Perm',
    field: 'observed',
    render: row => `${row.observed ? '✓' : '·'} / ${row.permitted ? '✓' : '·'}`,
    width: '10%',
  },
  {
    // Attribution firmness is rendered honestly: approximate/unknown are tinted
    // and never shown as firm (G8).
    title: 'Attribution',
    field: 'attribution_tier',
    render: row => {
      const a = attributionMeta(row.attribution_tier);
      return <ToneStatus tone={a.tone}>{a.label}</ToneStatus>;
    },
    width: '12%',
  },
  {
    title: 'Signals',
    field: 'signal_source',
    render: row => row.signal_sources || row.signal_source || '—',
  },
  { title: 'Last seen', field: 'last_seen', type: 'datetime' },
];

// The drift table renders a flattened row (rather than a nested DriftEntry) so
// every column has a real, typed, sortable/searchable field — and the honest
// tone is precomputed once per row.
interface DriftRow {
  finding: string;
  tone: Tone;
  origin: string;
  mode: string;
  resource: string;
  last_seen: string;
}

const toDriftRow = (entry: DriftEntry): DriftRow => ({
  finding: driftEntryLabel(entry),
  tone: driftEntryTone(entry),
  origin: originLabel(entry.edge),
  mode: entry.edge.mode,
  resource: resourceLabel(entry.edge),
  last_seen: entry.edge.last_seen,
});

const driftColumns: TableColumn<DriftRow>[] = [
  {
    title: 'Finding',
    field: 'finding',
    render: row => <ToneStatus tone={row.tone}>{row.finding}</ToneStatus>,
    width: '18%',
  },
  { title: 'Origin', field: 'origin', highlight: true },
  { title: 'Mode', field: 'mode', render: row => <ModeCell mode={row.mode} />, width: '8%' },
  { title: 'Resource', field: 'resource' },
  { title: 'Last seen', field: 'last_seen', type: 'datetime' },
];

/** The headline counts: graph topology + drift, with pending split out honestly. */
const HeaderStrip = ({ graph, diff }: { graph: GraphResponse; diff: DiffResponse }) => {
  const g = summarizeGraph(graph);
  const d = summarizeDrift(diff);
  return (
    <Grid container spacing={2}>
      <Grid item xs={12} md={6}>
        <InfoCard title="R/RW graph">
          <ToneStatus tone="default">
            {g.nodeCount} nodes · {g.edgeCount} edges
          </ToneStatus>
          <ToneStatus tone={g.writeEdgeCount > 0 ? 'warning' : 'ok'}>
            {g.writeEdgeCount} write / readwrite
          </ToneStatus>
          <ToneStatus tone="default">
            {g.observedCount} observed · {g.permittedCount} permitted
          </ToneStatus>
        </InfoCard>
      </Grid>
      <Grid item xs={12} md={6}>
        <InfoCard title="Least-privilege drift">
          <ToneStatus tone={d.firmUnexpected > 0 ? 'error' : 'ok'}>
            {d.firmUnexpected} unexpected access (firm)
          </ToneStatus>
          {d.pending > 0 && (
            <ToneStatus tone="pending">{d.pending} unexpected (reconciliation pending)</ToneStatus>
          )}
          <ToneStatus tone={d.unused > 0 ? 'warning' : 'ok'}>{d.unused} unused grants</ToneStatus>
        </InfoCard>
      </Grid>
    </Grid>
  );
};

/**
 * AccessMapContent renders the R/RW access map and the permitted-vs-observed
 * least-privilege drift (module III) over GET /access-map/graph and
 * GET /access-map/drift — both privileged, self-audited reads. It honors the
 * access-map UI contract: write edges get prominence, attribution that is only
 * `approximate`/`unknown` is never shown as firm, and a reconciliation-pending
 * unexpected access is amber ("pending"), never a red violation.
 */
export const AccessMapContent = () => {
  const api = useApi(olivaresApiRef);
  const graph = useAsyncData(() => api.accessGraph({ limit: 200 }), []);
  const drift = useAsyncData(() => api.accessDrift(), []);

  const error = graph.error || drift.error;
  if (error) {
    return (
      <Content>
        <ResponseErrorPanel error={error} />
      </Content>
    );
  }

  const loading = (graph.loading && !graph.value) || (drift.loading && !drift.value);
  const driftRows: DriftRow[] = drift.value
    ? [...drift.value.unexpected_accesses, ...drift.value.unused_grants].map(toDriftRow)
    : [];

  return (
    <Content>
      <ContentHeader title="Access map (R/RW)">
        <SupportButton>
          The agent→resource read/write graph and the permitted-vs-observed
          least-privilege drift, as the control plane reconciles it. Read-only and
          self-audited server-side.
        </SupportButton>
      </ContentHeader>

      {loading ? (
        <Progress />
      ) : (
        <Grid container spacing={2}>
          <Grid item xs={12}>
            {graph.value && drift.value && (
              <HeaderStrip graph={graph.value} diff={drift.value} />
            )}
          </Grid>
          <Grid item xs={12}>
            <Table<AccessEdge>
              title={`Access edges${graph.value?.has_more ? ' (first page)' : ''}`}
              options={{ search: true, paging: true, pageSize: 15, padding: 'dense' }}
              columns={edgeColumns}
              data={graph.value?.edges ?? []}
            />
          </Grid>
          <Grid item xs={12}>
            <Table<DriftRow>
              title="Least-privilege drift (unexpected access first)"
              options={{ search: true, paging: true, pageSize: 10, padding: 'dense' }}
              columns={driftColumns}
              data={driftRows}
            />
          </Grid>
        </Grid>
      )}
    </Content>
  );
};
