// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE SENTENCE ON THE WIRE, IN THE SHAPE THE RUN ACCEPTS.
//
// The operator types a turn. The engine's `/runs/{ref}/input` has two contracts:
// `{text}` for a driver that owns a protocol, `{line}` for the historical Claude
// stream-json child. This module chooses the body the same way the CLI's
// `agent session input --text` / `--line` pair does: a sentence is a turn, and a
// raw NDJSON line is only sent when the operator asked for the wire.
import type { RunDTO } from './types'
import { runInputMode } from './provider-contract'

/** The two request bodies `POST /runs/{ref}/input` accepts. */
export type SessionTurnBody = { text: string } | { line: string }

/**
 * Encode one operator sentence (or one raw wire line) as the input body this
 * run will accept.
 *
 * `wire` is the advanced disclosure: the typed value is sent as `{line}`
 * unchanged. The default path never asks the operator to write NDJSON — a
 * line-mode run wraps the sentence as a user frame, a text-mode run sends
 * `{text}` exactly as `agent session input --text` does.
 */
export function sessionTurnBody(
  run: Pick<RunDTO, 'provider_driver'>,
  value: string,
  wire = false,
): SessionTurnBody {
  if (wire) return { line: value }
  if (runInputMode(run) === 'text') return { text: value }
  return {
    line: JSON.stringify({
      type: 'user',
      message: { role: 'user', content: value },
    }),
  }
}
