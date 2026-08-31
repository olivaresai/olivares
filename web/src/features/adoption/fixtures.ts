// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic adoption fixtures shaped exactly like the backend DTOs. Used by the
// component tests and as honest sample data — never presented as real adoption metrics.
import type {
  AdoptionTotals,
  DiscrepancyResponse,
  DevelopersResponse,
  SummaryResponse,
  TeamsResponse,
  TrendResponse,
} from './types'

function totals(over: Partial<AdoptionTotals>): AdoptionTotals {
  return {
    sessions: 0,
    lines_added: 0,
    lines_removed: 0,
    lines_net: 0,
    commits: 0,
    pull_requests: 0,
    active_time_ms: 0,
    tools_accepted: 0,
    tools_rejected: 0,
    acceptance_rate: null,
    input_tokens: 0,
    output_tokens: 0,
    tokens: 0,
    ...over,
  }
}

export const summaryFixture: SummaryResponse = {
  since: '2026-05-21T00:00:00Z',
  analytics: {
    totals: totals({
      sessions: 412,
      lines_added: 86_400,
      lines_removed: 21_300,
      lines_net: 65_100,
      commits: 1_240,
      pull_requests: 318,
      tools_accepted: 9_820,
      tools_rejected: 2_180,
      acceptance_rate: 0.818,
      input_tokens: 142_000_000,
      output_tokens: 38_000_000,
      tokens: 180_000_000,
    }),
    by_model: [
      { model: 'claude-opus-4-8', tokens: 128_000_000 },
      { model: 'claude-sonnet-4-6', tokens: 52_000_000 },
    ],
    by_tool: [
      {
        tool: 'Edit',
        accepted: 7_100,
        rejected: 1_400,
        acceptance_rate: 0.835,
      },
      { tool: 'Write', accepted: 1_900, rejected: 520, acceptance_rate: 0.785 },
      {
        tool: 'MultiEdit',
        accepted: 620,
        rejected: 180,
        acceptance_rate: 0.775,
      },
      {
        tool: 'NotebookEdit',
        accepted: 200,
        rejected: 80,
        acceptance_rate: 0.714,
      },
    ],
  },
  telemetry: {
    totals: totals({
      sessions: 405,
      lines_added: 85_900,
      lines_removed: 21_100,
      lines_net: 64_800,
      commits: 1_232,
      pull_requests: 314,
      active_time_ms: 41_400_000,
      tools_accepted: 9_790,
      tools_rejected: 2_160,
      acceptance_rate: 0.819,
      input_tokens: 141_000_000,
      output_tokens: 37_800_000,
      tokens: 178_800_000,
    }),
    by_model: [
      { model: 'claude-opus-4-8', tokens: 127_000_000 },
      { model: 'claude-sonnet-4-6', tokens: 51_800_000 },
    ],
    by_tool: [
      {
        tool: 'Edit',
        accepted: 7_080,
        rejected: 1_390,
        acceptance_rate: 0.836,
      },
      { tool: 'Write', accepted: 1_910, rejected: 520, acceptance_rate: 0.786 },
    ],
  },
  developers: 37,
  teams: 5,
  boundary: {
    claude_api_only: true,
    excludes: [
      'claude-platform-aws',
      'microsoft-foundry',
      'amazon-bedrock',
      'vertex-ai',
    ],
  },
}

export const trendFixture: TrendResponse = {
  lens: 'analytics',
  days: [
    {
      day: '2026-05-21',
      totals: totals({
        sessions: 58,
        commits: 170,
        lines_net: 9_100,
        pull_requests: 44,
      }),
    },
    {
      day: '2026-05-22',
      totals: totals({
        sessions: 61,
        commits: 182,
        lines_net: 9_600,
        pull_requests: 47,
      }),
    },
    {
      day: '2026-05-23',
      totals: totals({
        sessions: 49,
        commits: 150,
        lines_net: 7_900,
        pull_requests: 39,
      }),
    },
  ],
  boundary: summaryFixture.boundary,
}

export const teamsFixture: TeamsResponse = {
  teams: [
    {
      team: 'payments',
      totals: totals({
        sessions: 160,
        commits: 480,
        lines_net: 26_000,
        pull_requests: 120,
        acceptance_rate: 0.84,
      }),
    },
    {
      team: 'platform',
      totals: totals({
        sessions: 120,
        commits: 360,
        lines_net: 19_000,
        pull_requests: 90,
        acceptance_rate: 0.81,
      }),
    },
    {
      team: '',
      totals: totals({
        sessions: 40,
        commits: 100,
        lines_net: 5_000,
        pull_requests: 20,
        acceptance_rate: 0.79,
      }),
    },
  ],
  boundary: summaryFixture.boundary,
}

export const developersFixture: DevelopersResponse = {
  developers: [
    {
      developer: 'ada@corp.example',
      totals: totals({
        sessions: 28,
        commits: 96,
        lines_net: 5_400,
        pull_requests: 24,
        acceptance_rate: 0.86,
      }),
    },
    {
      developer: 'lin@corp.example',
      totals: totals({
        sessions: 22,
        commits: 71,
        lines_net: 4_100,
        pull_requests: 18,
        acceptance_rate: 0.8,
      }),
    },
  ],
  boundary: summaryFixture.boundary,
}

export const discrepancyFixture: DiscrepancyResponse = {
  since: '2026-05-21T00:00:00Z',
  until: '2026-05-23T23:59:59Z',
  days: [
    {
      day: '2026-05-21',
      material: true,
      metrics: [
        {
          name: 'claude_code.session.count',
          analytics: 40,
          telemetry: 9,
          ratio: 0.775,
          direction: 'official_exceeds_telemetry',
          material: true,
        },
        {
          name: 'claude_code.token.usage',
          analytics: 90_000,
          telemetry: 0,
          ratio: 0,
          direction: 'official_exceeds_telemetry',
          material: false,
        },
        {
          name: 'claude_code.lines_of_code.count',
          analytics: 0,
          telemetry: 0,
          ratio: 0,
          direction: 'aligned',
          material: false,
        },
        {
          name: 'claude_code.commit.count',
          analytics: 0,
          telemetry: 0,
          ratio: 0,
          direction: 'aligned',
          material: false,
        },
        {
          name: 'claude_code.pull_request.count',
          analytics: 0,
          telemetry: 0,
          ratio: 0,
          direction: 'aligned',
          material: false,
        },
      ],
    },
    {
      day: '2026-05-22',
      material: true,
      metrics: [
        {
          name: 'claude_code.session.count',
          analytics: 0,
          telemetry: 0,
          ratio: 0,
          direction: 'aligned',
          material: false,
        },
        {
          name: 'claude_code.token.usage',
          analytics: 0,
          telemetry: 0,
          ratio: 0,
          direction: 'aligned',
          material: false,
        },
        {
          name: 'claude_code.lines_of_code.count',
          analytics: 0,
          telemetry: 600,
          ratio: 1,
          direction: 'official_plane_absent',
          material: true,
        },
        {
          name: 'claude_code.commit.count',
          analytics: 12,
          telemetry: 30,
          ratio: 0.6,
          direction: 'telemetry_exceeds_official',
          material: true,
        },
        {
          name: 'claude_code.pull_request.count',
          analytics: 0,
          telemetry: 0,
          ratio: 0,
          direction: 'aligned',
          material: false,
        },
      ],
    },
  ],
  thresholds: {
    ratio: 0.25,
    floors: {
      'claude_code.session.count': 10,
      'claude_code.token.usage': 100_000,
      'claude_code.lines_of_code.count': 500,
      'claude_code.commit.count': 10,
      'claude_code.pull_request.count': 5,
    },
  },
  boundary: summaryFixture.boundary,
}
