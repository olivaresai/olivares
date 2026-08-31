// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React from 'react';
import {
  Content,
  ContentHeader,
  Progress,
  ResponseErrorPanel,
  SupportButton,
  Table,
  TableColumn,
} from '@backstage/core-components';
import { useApi } from '@backstage/core-plugin-api';

import { olivaresApiRef } from '../../api/OlivaresApi';
import { useAsyncData } from '../../hooks/useAsyncData';
import { ToneStatus } from '../common/ToneStatus';
import {
  ccStateMeta,
  formatDuration,
  formatMicroUsd,
  formatTokens,
  modeToken,
} from '../../api/transform';
import type { LiveDTO } from '../../api/types';

const columns: TableColumn<LiveDTO>[] = [
  {
    title: 'State',
    field: 'cc_state',
    render: row => {
      const m = ccStateMeta(row.cc_state);
      return <ToneStatus tone={m.tone}>{m.label}</ToneStatus>;
    },
    width: '12%',
  },
  { title: 'Session', field: 'session_ref', highlight: true },
  { title: 'Agent', field: 'agent_ref', render: row => row.agent_ref || '—' },
  {
    title: 'Doing now',
    field: 'current_action',
    render: row =>
      row.current_action
        ? `${row.current_action}${
            row.current_resource
              ? ` ${modeToken(row.current_mode || 'unknown')} ${row.current_resource}`
              : ''
          }`
        : '—',
  },
  { title: 'Model', field: 'model_ref', render: row => row.model_ref || '—' },
  {
    title: 'Tokens (in/out)',
    field: 'input_tokens',
    type: 'numeric',
    render: row => `${formatTokens(row.input_tokens)} / ${formatTokens(row.output_tokens)}`,
  },
  {
    title: 'Cost',
    field: 'cost_micro_usd',
    type: 'numeric',
    render: row => formatMicroUsd(row.cost_micro_usd),
    width: '10%',
  },
  {
    title: 'Duration',
    field: 'duration_seconds',
    type: 'numeric',
    render: row => formatDuration(row.duration_seconds),
    width: '10%',
  },
  { title: 'Last event', field: 'last_event_at', type: 'datetime' },
];

/**
 * SessionsContent renders live operation (module II) over GET /sessions/live —
 * most-recently-active first. The session state (`cc_state`), cost and tokens are
 * all the engine's derived signals; the view never computes or fabricates them,
 * and an absent goal/summary simply shows "—" (carries no objective channel).
 * v1 fetches on mount with a manual refresh; the SSE stream is a later add.
 */
export const SessionsContent = () => {
  const api = useApi(olivaresApiRef);
  const sessions = useAsyncData(() => api.sessionsLive({ limit: 100 }), []);

  if (sessions.error) {
    return (
      <Content>
        <ResponseErrorPanel error={sessions.error} />
      </Content>
    );
  }

  return (
    <Content>
      <ContentHeader title="Live sessions">
        <SupportButton>
          What agents are doing right now — state, current action, tokens and cost —
          reconstructed by the control plane from the ingest stream. Read-only.
        </SupportButton>
      </ContentHeader>

      {sessions.loading && !sessions.value ? (
        <Progress />
      ) : (
        <Table<LiveDTO>
          title="Sessions (most recently active first)"
          options={{ search: true, paging: true, pageSize: 20, padding: 'dense' }}
          columns={columns}
          data={sessions.value?.items ?? []}
        />
      )}
    </Content>
  );
};
