// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

const src = resolve(__dirname, '../..')
const readers = [
  ['components/layout/session-rail-model.ts', 'sessionRow'],
  ['features/sessions/session-groups.ts', 'groupOf'],
  ['features/sessions/session-state-dot.tsx', 'StateDot'],
  ['features/sessions/session-state-word.tsx', 'StateWord'],
  ['features/sessions/session-state-badge.tsx', 'SessionStateBadge'],
]

// A raw engine state read in any spelling: x.state, x['state'] or { state } = x.
const RAW_STATE = ['state', 'cc_state']
function readsRawState(node: ts.Node): boolean {
  return (
    (ts.isPropertyAccessExpression(node) &&
      RAW_STATE.includes(node.name.text)) ||
    (ts.isElementAccessExpression(node) &&
      ts.isStringLiteralLike(node.argumentExpression) &&
      RAW_STATE.includes(node.argumentExpression.text)) ||
    (ts.isBindingElement(node) &&
      ts.isObjectBindingPattern(node.parent) &&
      RAW_STATE.includes((node.propertyName ?? node.name).getText()))
  )
}

describe('session display state has one classifier', () => {
  it.each(readers)(
    '%s imports and calls the classifier once',
    (path, renderer) => {
      const file = ts.createSourceFile(
        path,
        readFileSync(resolve(src, path), 'utf8'),
        ts.ScriptTarget.Latest,
        true,
      )
      let imports = 0
      let calls = 0
      const independent: string[] = []
      function visit(node: ts.Node) {
        // Streaming availability is a process control, not displayed state.
        if (
          ts.isFunctionDeclaration(node) &&
          node.name?.text === 'showsLiveDot'
        )
          return
        if (
          ts.isImportSpecifier(node) &&
          node.name.text === 'sessionDisplayState'
        )
          imports++
        if (
          ts.isCallExpression(node) &&
          ts.isIdentifier(node.expression) &&
          node.expression.text === 'sessionDisplayState'
        )
          calls++
        if (
          readsRawState(node) &&
          !(
            ts.isPropertyAccessExpression(node) &&
            ['row', 'h.handoff'].includes(node.expression.getText(file))
          )
        )
          independent.push(node.getText(file))
        ts.forEachChild(node, visit)
      }
      visit(file)
      expect({ imports, calls, independent }, renderer).toEqual({
        imports: 1,
        calls: 1,
        independent: [],
      })
    },
  )

  // The list also sorts and filters by state: its sort accessor must call the shared
  // projection too, so it is the one view with a call of its own.
  it.each([
    // The list says its state as a word with its time (dev#3162).
    ['features/sessions/work-rail.tsx', 'StateWord', 0],
    ['features/sessions/thread-header.tsx', 'StateDot', 0],
    ['features/sessions/sessions-workspace-view.tsx', 'SessionStateBadge', 1],
  ])('%s renders its state through %s', (path, component, projections) => {
    const file = ts.createSourceFile(
      path,
      readFileSync(resolve(src, path), 'utf8'),
      ts.ScriptTarget.Latest,
      true,
    )
    let imported = false
    let rendered = false
    let calls = 0
    const independent: string[] = []
    function visit(node: ts.Node) {
      // These commands control the live process; they do not label its state.
      if (
        ts.isFunctionDeclaration(node) &&
        node.name?.text === 'terminalCommands'
      )
        return
      if (ts.isImportSpecifier(node) && node.name.text === component)
        imported = true
      if (
        (ts.isJsxSelfClosingElement(node) || ts.isJsxOpeningElement(node)) &&
        node.tagName.getText(file) === component
      )
        rendered = true
      if (
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        node.expression.text === 'sessionDisplayState'
      )
        calls++
      if (readsRawState(node)) {
        // The list's explicit state filter compares raw facts to a user-selected
        // value. Its state cell and sort accessor must use the shared projection.
        const filter =
          ts.isPropertyAccessExpression(node) &&
          ts.isBinaryExpression(node.parent) &&
          node.parent.right.getText(file) === 'value'
        if (!filter) independent.push(node.getText(file))
      }
      ts.forEachChild(node, visit)
    }
    visit(file)
    expect({ imported, rendered, calls, independent }).toEqual({
      imported: true,
      rendered: true,
      calls: projections,
      independent: [],
    })
  })

  // The Business overlay is present when this test runs on the composed console.
  const cockpit = resolve(src, 'features/session-cockpit/workspace-groups.ts')
  it.skipIf(!existsSync(cockpit))(
    'the cockpit chip uses the Community classifier adapters',
    () => {
      const file = ts.createSourceFile(
        cockpit,
        readFileSync(cockpit, 'utf8'),
        ts.ScriptTarget.Latest,
        true,
      )
      for (const [adapter, classifier] of [
        ['runRailState', 'railGroups'],
        ['runGroup', 'groupOf'],
      ]) {
        let imports = 0
        let calls = 0
        const independent: string[] = []
        function visit(node: ts.Node, display = false) {
          if (ts.isFunctionDeclaration(node))
            display = node.name?.text === adapter
          if (ts.isImportSpecifier(node) && node.name.text === classifier)
            imports++
          if (
            display &&
            ts.isCallExpression(node) &&
            ts.isIdentifier(node.expression) &&
            node.expression.text === classifier
          )
            calls++
          if (
            display &&
            ts.isBinaryExpression(node) &&
            /\b(state|cc_state)\b/.test(node.getText(file))
          )
            independent.push(node.getText(file))
          ts.forEachChild(node, (child) => visit(child, display))
        }
        visit(file)
        expect({ imports, calls, independent }, adapter).toEqual({
          imports: 1,
          calls: 1,
          independent: [],
        })
      }
      for (const view of ['thread-view.tsx', 'workspace-sidebar.tsx']) {
        const path = resolve(src, 'features/session-cockpit', view)
        const source = ts.createSourceFile(
          path,
          readFileSync(path, 'utf8'),
          ts.ScriptTarget.Latest,
          true,
        )
        let imports = 0
        const chips: string[] = []
        function visit(node: ts.Node) {
          if (ts.isImportSpecifier(node) && node.name.text === 'runRailState')
            imports++
          if (
            ts.isJsxSelfClosingElement(node) &&
            node.tagName.getText(source) === 'RailGlyph'
          ) {
            for (const attribute of node.attributes.properties) {
              if (
                ts.isJsxAttribute(attribute) &&
                attribute.name.getText(source) === 'state'
              )
                chips.push(attribute.initializer?.getText(source) ?? '')
            }
          }
          ts.forEachChild(node, visit)
        }
        visit(source)
        expect({ imports, chips }, view).toEqual({
          imports: 1,
          chips: ['{runRailState(run)}'],
        })
      }
    },
  )
})
