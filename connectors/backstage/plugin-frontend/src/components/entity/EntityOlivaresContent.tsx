// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React, { useMemo } from 'react';
import { Grid } from '@material-ui/core';
import {
  EmptyState,
  InfoCard,
  Progress,
  ResponseErrorPanel,
  StructuredMetadataTable,
  Table,
  TableColumn,
} from '@backstage/core-components';
import { useApi } from '@backstage/core-plugin-api';
import { useEntity } from '@backstage/plugin-catalog-react';

import { olivaresApiRef } from '../../api/OlivaresApi';
import { useAsyncData } from '../../hooks/useAsyncData';
import { ToneStatus } from '../common/ToneStatus';
import { filterSessionsForAgent, matchAgentToEntity } from './olivaresEntity';
import {
  attributionMeta,
  ccStateMeta,
  formatMicroUsd,
  isWriteMode,
  modeToken,
  summarizeGraph,
} from '../../api/transform';
import type { AccessEdge, GraphResponse, LiveDTO } from '../../api/types';

const sessionColumns: TableColumn<LiveDTO>[] = [
  {
    title: 'State',
    field: 'cc_state',
    render: row => {
      const m = ccStateMeta(row.cc_state);
      return <ToneStatus tone={m.tone}>{m.label}</ToneStatus>;
    },
  },
  { title: 'Session', field: 'session_ref', highlight: true },
  { title: 'Doing now', field: 'current_action', render: row => row.current_action || '—' },
  { title: 'Cost', field: 'cost_micro_usd', render: row => formatMicroUsd(row.cost_micro_usd) },
  { title: 'Last event', field: 'last_event_at', type: 'datetime' },
];

const edgeColumns: TableColumn<AccessEdge>[] = [
  {
    title: 'Mode',
    field: 'mode',
    render: row => (
      <ToneStatus tone={isWriteMode(row.mode) ? 'warning' : 'default'}>
        {modeToken(row.mode)}
      </ToneStatus>
    ),
    width: '8%',
  },
  {
    title: 'Resource',
    field: 'resource_ref',
    render: row => row.resource_ref || `${row.resource_kind || 'resource'}:${row.resource_id}`,
    highlight: true,
  },
  {
    title: 'Obs / Perm',
    field: 'observed',
    render: row => `${row.observed ? '✓' : '·'} / ${row.permitted ? '✓' : '·'}`,
  },
  {
    title: 'Attribution',
    field: 'attribution_tier',
    render: row => {
      const a = attributionMeta(row.attribution_tier);
      return <ToneStatus tone={a.tone}>{a.label}</ToneStatus>;
    },
  },
  { title: 'Last seen', field: 'last_seen', type: 'datetime' },
];

/**
 * EntityOlivaresContent is the entity-page tab that brings an agent's Olivares
 * governance into the developer's flow: viewing the Component, they see its live
 * sessions and its R/RW access without leaving the catalog. It resolves the agent
 * from the entity (firm external-id annotation, else the slugged name) and queries
 * the control plane by that agent's id — honest matching, no guessing: an entity
 * with no matching agent shows an explicit "not linked" state.
 */
export const EntityOlivaresContent = () => {
  const { entity } = useEntity();
  const api = useApi(olivaresApiRef);

  const agentsState = useAsyncData(() => api.agents({ limit: 500 }), []);
  const agent = useMemo(
    () => (agentsState.value ? matchAgentToEntity(entity, agentsState.value.items) : undefined),
    [agentsState.value, entity],
  );

  // Dependent reads always run (returning empty when there is no agent) so the
  // hook order is stable across renders.
  const sessionsState = useAsyncData<LiveDTO[]>(async () => {
    if (!agent) return [];
    const r = await api.sessionsLive({ limit: 200 });
    return filterSessionsForAgent(r.items, agent);
  }, [agent?.id]);

  const graphState = useAsyncData<GraphResponse>(async () => {
    if (!agent) return { nodes: [], edges: [], has_more: false };
    return api.accessGraph({ origin_id: agent.id, limit: 200 });
  }, [agent?.id]);

  if (agentsState.error) {
    return <ResponseErrorPanel error={agentsState.error} />;
  }
  if (agentsState.loading && !agentsState.value) {
    return <Progress />;
  }
  if (!agent) {
    return (
      <EmptyState
        missing="info"
        title="Not linked to an Olivares agent"
        description="This entity has no matching agent in the Olivares control plane. Agents are linked by the olivares.ai/external-id annotation, or by a Component name that matches the agent name."
      />
    );
  }

  const graphSummary = graphState.value ? summarizeGraph(graphState.value) : undefined;

  return (
    <Grid container spacing={3}>
      <Grid item xs={12} md={4}>
        <InfoCard title="Olivares agent">
          <StructuredMetadataTable
            metadata={{
              Status: <ToneStatus tone="ok">{agent.status}</ToneStatus>,
              Kind: agent.kind || '—',
              'External id': agent.external_id || '—',
              'Identity id': agent.identity_id || '—',
              Access: graphSummary
                ? `${graphSummary.edgeCount} edges · ${graphSummary.writeEdgeCount} write`
                : '…',
            }}
          />
        </InfoCard>
      </Grid>

      <Grid item xs={12} md={8}>
        <InfoCard title="Live sessions">
          {sessionsState.loading && !sessionsState.value ? (
            <Progress />
          ) : (
            <Table<LiveDTO>
              options={{ search: false, paging: true, pageSize: 5, padding: 'dense', toolbar: false }}
              columns={sessionColumns}
              data={sessionsState.value ?? []}
              emptyContent={<EmptyState missing="data" title="No sessions for this agent" />}
            />
          )}
        </InfoCard>
      </Grid>

      <Grid item xs={12}>
        <InfoCard title="Access (R/RW)">
          {graphState.loading && !graphState.value ? (
            <Progress />
          ) : (
            <Table<AccessEdge>
              options={{ search: false, paging: true, pageSize: 10, padding: 'dense', toolbar: false }}
              columns={edgeColumns}
              data={graphState.value?.edges ?? []}
              emptyContent={<EmptyState missing="data" title="No observed or permitted access yet" />}
            />
          )}
        </InfoCard>
      </Grid>
    </Grid>
  );
};
