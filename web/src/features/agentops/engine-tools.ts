// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The tools this engine knows, as its own driver table says (GET /v1/m/sessions/tools,
// core/driverfacts; read by engineToolsQuery in ./api). The console names, offers and binds tools from that answer and keeps
// no list of its own, so a new tool row needs no console change. The shell reads it once
// before it paints (AppLayout), so every reader below answers from that read; a failed
// read is said in the status bar, with Retry.

/** A provider kind a tool runs on, with the endpoint rule the engine applies to it. */
export interface ToolBinding {
  kind: string
  egress: string
}

export interface EngineTool {
  key: string
  /** The tool's own product name (never translated). */
  name: string
  /** The word an account of the tool is named from (`gemini` for Gemini CLI). */
  name_stem: string
  /** A session can run on it. */
  session: boolean
  bindings: ToolBinding[]
}

/** A driver key the engine lists as a session tool. */
export type SessionTool = string

let known: readonly EngineTool[] = []

/** Takes the engine's answer: the shell's read, or a test's fixture. */
export function setEngineTools(tools: readonly EngineTool[]): void {
  known = tools
}

/** A row of the engine's answer, as this table takes it. */
export const isEngineTool = (row: unknown): row is EngineTool => {
  const tool = row as EngineTool | null
  return (
    typeof tool?.key === 'string' &&
    typeof tool.name === 'string' &&
    typeof tool.name_stem === 'string' &&
    typeof tool.session === 'boolean' &&
    Array.isArray(tool.bindings) &&
    tool.bindings.every(
      (b) => typeof b?.kind === 'string' && typeof b.egress === 'string',
    )
  )
}

export function engineTool(driver: string): EngineTool | undefined {
  return known.find((tool) => tool.key === driver)
}

/** The tools a session can start on, in the engine's order. */
export function sessionTools(): SessionTool[] {
  return known.filter((tool) => tool.session).map((tool) => tool.key)
}

/** A driver's product name; a driver the engine does not list is named by itself. */
export function toolName(driver: string): string {
  return engineTool(driver)?.name ?? driver
}
