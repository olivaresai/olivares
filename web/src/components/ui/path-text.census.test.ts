// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EVERY RAW PATH IN THE CONSOLE RENDERS THROUGH `PathText` (#3135).
//
// A path written straight into the page as `{file.path}` is cut wherever its own cell happens
// to cut it, has no hover with the whole value and no copy: the defect the regression fixed on the
// Sessions page one site at a time. `PathText` is the one way to show a filesystem path or a
// branch name, and this census is the proof that nothing else does.
//
// It parses every component file (the TypeScript compiler, not a pattern: a pattern cannot tell
// a JSX child from an object shorthand) and lists path fields shown as JSX children or inside
// translated sentences: `{f.path}`, `{run.workspace_path}`, `{git.branch}`, `t('x', { cwd })`.
// String-only accessible names (`aria-label`) are excluded; a custom component's `title` prop
// is included when SectionCard or ConfirmDialog renders it. Declared rows below are non-paths
// that only resemble one by name (an API route, a JSON pointer, an environment variable), or
// the text of an <option>. A declared row that no longer matches is a failure too.
//
// A folder's NAME is not a path and is not scanned: `session.folder` on a live card or in Recent
// work is `sessionFolder()`, the last segment. `folder` alone is too common a name to scan for;
// the folder selector's own test proves its two PathText sites instead.
//
// The scan sees direct fields and the `_path`, `_dir`, `_home` and branch families through `??`,
// `||`, `&&`, `?:`, `!`, `as`, templates, arrays and translation options. It also follows the
// declared path helpers (`fileTitle`, `folderLabel`), managed-path constants and `Trans`
// values without an equivalent PathText slot. A path cut into a display string by a helper
// (`.ts` or `.tsx`) before render is the last check below; state-backed aliases stay a review
// point; table cells and selected-file state have their own checks.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { walk } from '@/test/empty-state-census'

const SRC = resolve(__dirname, '../..')

/** The properties that hold a filesystem path or a branch name. The suffixes catch the
 * whole family (`config_dir`, `user_home`, `source_path`), not only sites already found. */
const PATH_FIELDS = new Set([
  'path',
  'cwd',
  'dir',
  'directory',
  'worktree_branch',
  'base_branch',
  'head_branch',
  'branch',
])
const PATH_SUFFIX = /(?:_path|_dir|_home|Path|Dir|Home|_branch|Branch)$/
const PATH_HELPERS = new Set(['fileTitle', 'folderLabel'])
const MANAGED_PATH_OBJECTS = new Set([
  'MANAGED_SETTINGS_PATHS',
  'MANAGED_MCP_PATHS',
])
const MANAGED_PATH_FIELDS = new Set([
  'macos',
  'linux',
  'windows',
  'windowsLegacyRemoved',
])

function isPathField(field: string): boolean {
  return PATH_FIELDS.has(field) || PATH_SUFFIX.test(field)
}

function isPathHelper(node: ts.Node): node is ts.CallExpression {
  return (
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    PATH_HELPERS.has(node.expression.text)
  )
}

function isManagedPath(node: ts.Node): boolean {
  if (
    ts.isPropertyAccessExpression(node) &&
    ts.isIdentifier(node.expression) &&
    MANAGED_PATH_OBJECTS.has(node.expression.text)
  )
    return MANAGED_PATH_FIELDS.has(node.name.text)
  return (
    ts.isElementAccessExpression(node) &&
    ts.isIdentifier(node.expression) &&
    MANAGED_PATH_OBJECTS.has(node.expression.text) &&
    ts.isStringLiteral(node.argumentExpression) &&
    MANAGED_PATH_FIELDS.has(node.argumentExpression.text)
  )
}

function translationNames(file: ts.SourceFile): Set<string> {
  const names = new Set(['t'])
  const visit = (node: ts.Node) => {
    if (ts.isVariableDeclaration(node) && ts.isObjectBindingPattern(node.name))
      for (const element of node.name.elements)
        if (
          element.propertyName?.getText(file) === 't' &&
          ts.isIdentifier(element.name)
        )
          names.add(element.name.text)
    ts.forEachChild(node, visit)
  }
  visit(file)
  return names
}

function isTranslation(
  node: ts.Node,
  names: Set<string>,
): node is ts.CallExpression {
  if (!ts.isCallExpression(node)) return false
  if (ts.isIdentifier(node.expression)) return names.has(node.expression.text)
  return (
    ts.isPropertyAccessExpression(node.expression) &&
    node.expression.name.text === 't'
  )
}

const STRING_ATTRIBUTES = new Set([
  'aria-label',
  'aria-description',
  'aria-valuetext',
  'placeholder',
  'alt',
  'textValue',
])

/** A translated accessible name is still a string, not a place for a component. A custom
 * component's `title` prop is different: SectionCard and ConfirmDialog render it visibly. */
function isAccessibleText(node: ts.Node): boolean {
  for (let current = node.parent; current; current = current.parent) {
    if (!ts.isJsxAttribute(current)) continue
    const name = current.name.getText()
    if (name === 'children') return false
    const opening = current.parent.parent
    // A native element's attribute is always text: no component can go there.
    if (/^\p{Ll}/u.test(opening.tagName.getText())) return true
    // SectionCard and ConfirmDialog render their title; the others are string-only.
    if (name === 'title') return false
    return STRING_ATTRIBUTES.has(name)
  }
  return false
}

/** Sites that name a path field but show something else, each with the reason and how many. */
const DECLARED: ReadonlyArray<{
  file: string
  expr: string
  count: number
  why: string
}> = [
  {
    file: 'features/attestation/components.tsx',
    expr: 'airgap.no_phone_home',
    count: 1,
    why: 'a human-readable attestation claim, not a filesystem path',
  },
  {
    file: 'features/claude-policy/references.tsx',
    expr: 'PLUGIN_MARKETPLACE_GOVERNANCE.airGap.seedDir',
    count: 1,
    why: 'an environment variable and its explanation, not a filesystem path',
  },
  {
    file: 'features/claude-policy/references.tsx',
    expr: 'PLUGIN_MARKETPLACE_GOVERNANCE.airGap.cacheDir',
    count: 1,
    why: 'an environment variable and its explanation, not a filesystem path',
  },
  {
    file: 'features/api-playground/endpoint-tree.tsx',
    expr: 'ep.path',
    count: 1,
    why: 'an API route template (/v1/...), not a filesystem path',
  },
  {
    file: 'features/api-playground/request-history.tsx',
    expr: 'entry.path',
    count: 1,
    why: 'an API route the person called, not a filesystem path',
  },
  {
    file: 'features/attestation/components.tsx',
    expr: 'binary.main_module.path',
    count: 1,
    why: 'a Go module path (an identifier like github.com/org/repo), not a filesystem path',
  },
  {
    file: 'features/claude-policy/components.tsx',
    expr: 'issue.path',
    count: 1,
    why: 'a JSON pointer into the policy document, not a filesystem path',
  },
  {
    file: 'features/claude-policy/policy-authoring-panel.tsx',
    expr: 'c.path',
    count: 1,
    why: 'a JSON pointer into the policy document, not a filesystem path',
  },
  {
    file: 'features/sessions/session-git.tsx',
    expr: 'data.branch',
    count: 1,
    why: 'the text of an <option>, which can only hold a string',
  },
]

/** What an expression can put on the page: through `??`, `||`, `&&` (its right side), `?:`,
 * `!`, `as`, parentheses, templates, arrays and translation option objects. */
function shown(node: ts.Expression): ts.Expression[] {
  if (
    ts.isParenthesizedExpression(node) ||
    ts.isNonNullExpression(node) ||
    ts.isAsExpression(node)
  )
    return shown(node.expression)
  if (ts.isConditionalExpression(node))
    return [...shown(node.whenTrue), ...shown(node.whenFalse)]
  if (ts.isTemplateExpression(node))
    return node.templateSpans.flatMap((span) => shown(span.expression))
  if (ts.isArrayLiteralExpression(node))
    return node.elements.flatMap((element) =>
      ts.isSpreadElement(element) ? shown(element.expression) : shown(element),
    )
  if (ts.isObjectLiteralExpression(node))
    return node.properties.flatMap((property) => {
      if (ts.isPropertyAssignment(property)) return shown(property.initializer)
      if (ts.isShorthandPropertyAssignment(property)) return [property.name]
      if (ts.isSpreadAssignment(property)) return shown(property.expression)
      return []
    })
  if (ts.isBinaryExpression(node)) {
    const op = node.operatorToken.kind
    if (op === ts.SyntaxKind.AmpersandAmpersandToken) return shown(node.right)
    if (op === ts.SyntaxKind.PlusToken)
      return [...shown(node.left), ...shown(node.right)]
    if (
      op === ts.SyntaxKind.QuestionQuestionToken ||
      op === ts.SyntaxKind.BarBarToken
    )
      return [...shown(node.left), ...shown(node.right)]
  }
  return [node]
}

/** The last name of a read: `file.path` -> `path`, `file['path']` -> `path`. */
function fieldOf(node: ts.Expression): string | undefined {
  if (ts.isIdentifier(node)) return node.text
  if (ts.isPropertyAccessExpression(node)) return node.name.text
  if (
    ts.isElementAccessExpression(node) &&
    ts.isStringLiteral(node.argumentExpression)
  )
    return node.argumentExpression.text
  return undefined
}

function isPathLeaf(node: ts.Expression): boolean {
  const field = fieldOf(node)
  return (
    isPathHelper(node) ||
    isManagedPath(node) ||
    (field !== undefined && isPathField(field))
  )
}

function attribute(
  opening: ts.JsxOpeningElement | ts.JsxSelfClosingElement,
  name: string,
) {
  return opening.attributes.properties.find(
    (property): property is ts.JsxAttribute =>
      ts.isJsxAttribute(property) && property.name.getText() === name,
  )
}

function attributeExpression(attribute: ts.JsxAttribute | undefined) {
  return attribute?.initializer && ts.isJsxExpression(attribute.initializer)
    ? attribute.initializer.expression
    : undefined
}

function pathTextValues(node: ts.Node, file: ts.SourceFile): string[] {
  const values: string[] = []
  const visit = (current: ts.Node) => {
    if (ts.isJsxElement(current) || ts.isJsxSelfClosingElement(current)) {
      const opening = ts.isJsxElement(current)
        ? current.openingElement
        : current
      if (opening.tagName.getText() === 'PathText') {
        const value = attributeExpression(attribute(opening, 'value'))
        if (value) values.push(value.getText(file))
      }
    }
    ts.forEachChild(current, visit)
  }
  visit(node)
  return values
}

function tableCellUsesPathText(
  cell: ts.Node,
  accessor: string,
  file: ts.SourceFile,
): boolean {
  const accepted = new Set([
    'getValue()',
    'getValue<string>()',
    `row.original.${accessor}`,
  ])
  const values = pathTextValues(cell, file).map((value) =>
    value.replace(/\s/g, ''),
  )
  if (!values.some((value) => accepted.has(value))) return false
  let raw = false
  const visit = (node: ts.Node) => {
    // Guards choose what renders; only their output branches can show a value.
    if (ts.isConditionalExpression(node)) {
      visit(node.whenTrue)
      visit(node.whenFalse)
      return
    }
    if (
      ts.isBinaryExpression(node) &&
      node.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken
    ) {
      visit(node.right)
      return
    }
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const opening = ts.isJsxElement(node) ? node.openingElement : node
      if (opening.tagName.getText(file) === 'PathText') return
    }
    if (
      ts.isJsxExpression(node) &&
      node.expression &&
      ts.isJsxAttribute(node.parent) &&
      node.parent.name.getText(file) === 'value'
    ) {
      for (const leaf of shown(node.expression)) {
        if (
          leaf.getText(file).replace(/\s/g, '') === `row.original.${accessor}`
        )
          raw = true
      }
    }
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === 'getValue'
    )
      raw = true
    ts.forEachChild(node, visit)
  }
  visit(cell)
  return !raw
}

/** The JSX children and translated sentences of a source file that show a path. */
export function rawPathSites(
  source: string,
): Array<{ expr: string; line: number }> {
  const file = ts.createSourceFile(
    'x.tsx',
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  )
  const translations = translationNames(file)
  const found: Array<{ expr: string; line: number }> = []
  const add = (leaf: ts.Expression, at: ts.Node) => {
    if (!isPathLeaf(leaf)) return
    found.push({
      expr: leaf.getText(file),
      line: file.getLineAndCharacterOfPosition(at.getStart(file)).line + 1,
    })
  }
  const visit = (node: ts.Node) => {
    if (
      ts.isPropertyAssignment(node) &&
      node.name.getText(file) === 'accessorKey' &&
      ts.isStringLiteral(node.initializer) &&
      isPathField(node.initializer.text)
    ) {
      const cell = node.parent.properties.find(
        (property) => property.name?.getText(file) === 'cell',
      )
      if (!cell || !tableCellUsesPathText(cell, node.initializer.text, file))
        found.push({
          expr: `${node.initializer.text} table cell`,
          line:
            file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1,
        })
    }
    if (
      ts.isJsxExpression(node) &&
      node.expression &&
      (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))
    )
      for (const leaf of shown(node.expression)) add(leaf, node)
    if (isTranslation(node, translations) && !isAccessibleText(node))
      for (const argument of node.arguments)
        for (const leaf of shown(argument)) add(leaf, node)
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      if (node.tagName.getText() === 'Trans') {
        const values = attributeExpression(attribute(node, 'values'))
        const components = attributeExpression(attribute(node, 'components'))
        const paths = values ? shown(values).filter(isPathLeaf) : []
        const copies = components ? pathTextValues(components, file) : []
        const missing = paths.filter(
          (leaf) => !copies.includes(leaf.getText(file)),
        )
        for (const leaf of missing) add(leaf, node)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return found
}

function jsxAttributeEnabled(attribute: ts.JsxAttribute | undefined): boolean {
  if (!attribute) return false
  if (!attribute.initializer) return true
  return attributeExpression(attribute)?.kind === ts.SyntaxKind.TrueKeyword
}

function pathTextCalls(
  source: string,
): Array<{ value: string; wrap: boolean }> {
  const file = ts.createSourceFile(
    'x.tsx',
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  )
  const calls: Array<{ value: string; wrap: boolean }> = []
  const visit = (node: ts.Node) => {
    if (ts.isJsxSelfClosingElement(node) || ts.isJsxOpeningElement(node)) {
      if (node.tagName.getText(file) === 'PathText')
        calls.push({
          value:
            attributeExpression(attribute(node, 'value'))?.getText(file) ?? '',
          wrap: jsxAttributeEnabled(attribute(node, 'wrap')),
        })
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return calls
}

const sources = walk(SRC, ['.tsx']).filter((f) => !/\.test\.tsx$/.test(f))

describe('every raw path renders through PathText', () => {
  it('has no path or branch written straight into the page', () => {
    const raw: string[] = []
    for (const file of sources) {
      const rel = relative(SRC, file).split('\\').join('/')
      for (const { expr, line } of rawPathSites(readFileSync(file, 'utf8')))
        if (!DECLARED.some((d) => d.file === rel && d.expr === expr))
          raw.push(`${rel}:${line}  {${expr}}`)
    }
    expect(raw).toEqual([])
  })

  it('declares exactly the sites that are there: not fewer, and not one more', () => {
    // A second `{data.branch}` beside the declared one is a new site, not the old one again.
    const drift = DECLARED.flatMap((d) => {
      const there = rawPathSites(
        readFileSync(resolve(SRC, d.file), 'utf8'),
      ).filter((s) => s.expr === d.expr).length
      return there === d.count
        ? []
        : [`${d.file} {${d.expr}}: declared ${d.count}, found ${there}`]
    })
    expect(drift).toEqual([])
  })

  it('finds a child and leaves attributes and object shorthand alone', () => {
    // The control positive: without it a scanner that finds nothing reads as clean.
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(sites(`const A = () => <td>{file.path}</td>`)).toEqual(['file.path'])
    expect(sites(`const A = () => <p><Mono>{git.branch}</Mono></p>`)).toEqual([
      'git.branch',
    ])
    expect(sites(`const A = () => <>{cwd}</>`)).toEqual(['cwd'])
    expect(sites(`const A = () => <td title={file.path}>x</td>`)).toEqual([])
    expect(sites(`const A = () => <PathText value={file.path} />`)).toEqual([])
    expect(sites(`const s = { path }; use({ cwd })`)).toEqual([])
    expect(sites(`const A = () => <p>{session.folder}</p>`)).toEqual([])
  })

  it('finds suffix fields, translated sentences, templates and path helpers', () => {
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(sites(`const A = () => <p>{snap.config_dir}</p>`)).toEqual([
      'snap.config_dir',
    ])
    expect(sites(`const A = () => <p>{data.config_home}</p>`)).toEqual([
      'data.config_home',
    ])
    expect(sites(`const A = () => <p>{license.source_path}</p>`)).toEqual([
      'license.source_path',
    ])
    expect(sites(`const A = () => <p>{t('x', { cwd })}</p>`)).toEqual(['cwd'])
    expect(sites(`const s = t('reach', { path: f.path })`)).toEqual(['f.path'])
    expect(
      sites(`const A = () => <p>{\`${'${w.name} (${w.root_path})'}\`}</p>`),
    ).toEqual(['w.root_path'])
    expect(sites(`const A = () => <p>{fileTitle(file)}</p>`)).toEqual([
      'fileTitle(file)',
    ])
    expect(
      sites(`const title = fileTitle(file); const A = () => <p>{title}</p>`),
    ).toEqual([])
    expect(sites(`const A = () => <p>{folderLabel(w)}</p>`)).toEqual([
      'folderLabel(w)',
    ])
    expect(
      sites(`const A = () => <Button aria-label={t('x', { cwd })} />`),
    ).toEqual([])
    expect(
      sites(`const A = () => <Section title={t('x', { cwd })} />`),
    ).toEqual(['cwd'])
  })

  it('finds computed fields and renamed translation calls, but not string-only attributes', () => {
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(sites(`const A = () => <p>{file['path']}</p>`)).toEqual([
      "file['path']",
    ])
    expect(
      sites(
        `const { t: tr } = useTranslation(); const A = () => <p>{tr('x', { cwd })}</p>`,
      ),
    ).toEqual(['cwd'])
    expect(sites(`const A = () => <p>{i18n.t('x', { cwd })}</p>`)).toEqual([
      'cwd',
    ])
    expect(
      sites(`const A = () => <input placeholder={t('x', { cwd })} />`),
    ).toEqual([])
    expect(sites(`const A = () => <div children={t('x', { cwd })} />`)).toEqual(
      ['cwd'],
    )
  })

  it('requires a PathText component for every path interpolated into Trans', () => {
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(sites(`const A = () => <Trans values={{ cwd }} />`)).toEqual(['cwd'])
    expect(
      sites(
        `const A = () => <Trans values={{ cwd }} components={{ path: <span><PathText value={cwd} /></span> }} />`,
      ),
    ).toEqual([])
    expect(
      sites(
        `const other = '/elsewhere'; const A = () => <Trans values={{ cwd }} components={{ path: <PathText value={other} /> }} />`,
      ),
    ).toEqual(['cwd'])
  })

  it('covers the managed-path constants and the legacy path slot in every locale', () => {
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(sites(`const A = () => <p>{MANAGED_MCP_PATHS.macos}</p>`)).toEqual([
      'MANAGED_MCP_PATHS.macos',
    ])
    expect(
      sites(`const A = () => <p>{MANAGED_SETTINGS_PATHS.windows}</p>`),
    ).toEqual(['MANAGED_SETTINGS_PATHS.windows'])
    expect(
      sites(`const A = () => <p>{MANAGED_MCP_PATHS['macos']}</p>`),
    ).toEqual(["MANAGED_MCP_PATHS['macos']"])
    const catalogs = walk(resolve(SRC, 'features/claude-policy/i18n'), [
      '.json',
    ])
    expect(catalogs).toHaveLength(9)
    for (const file of catalogs) {
      const catalog = JSON.parse(readFileSync(file, 'utf8'))
      expect(catalog.ref.ms.legacyPath, file).toContain('<path />')
      expect(catalog.ref.ms.legacyPath, file).not.toContain('C:\\')
    }
  })

  it('sees a path through the wrappers a child can put around it', () => {
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(sites(`const A = () => <td>{file.path ?? '-'}</td>`)).toEqual([
      'file.path',
    ])
    expect(sites(`const A = () => <td>{cwd || 'n/a'}</td>`)).toEqual(['cwd'])
    expect(
      sites(`const A = () => <td>{ok && run.workspace_path}</td>`),
    ).toEqual(['run.workspace_path'])
    expect(sites(`const A = () => <td>{ok ? a.branch : b.dir}</td>`)).toEqual([
      'a.branch',
      'b.dir',
    ])
    expect(sites(`const A = () => <td>{(f.path)!}</td>`)).toEqual(['f.path'])
    expect(sites(`const A = () => <td>{f.path as string}</td>`)).toEqual([
      'f.path',
    ])
    expect(sites(`const A = () => <td>{b.folder_path}</td>`)).toEqual([
      'b.folder_path',
    ])
    // The condition of `&&` is not shown; only what follows it is.
    expect(sites(`const A = () => <td>{cwd && <b />}</td>`)).toEqual([])
  })

  it('requires PathText in a table cell backed by a path field', () => {
    const sites = (code: string) => rawPathSites(code).map((s) => s.expr)
    expect(
      sites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ getValue }) => <span>{getValue<string>()}</span> }]`,
      ),
    ).toEqual(['root_path table cell'])
    expect(
      sites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ getValue }) => <PathText value={getValue<string>()} /> }]`,
      ),
    ).toEqual([])
    expect(
      sites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ getValue }) => <><PathText value={getValue<string>()} /><span>{getValue<string>()}</span></> }]`,
      ),
    ).toEqual(['root_path table cell'])
    expect(
      sites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ row }) => <><PathText value={row.original.root_path} /><span>{row.original.root_path}</span></> }]`,
      ),
    ).toEqual(['row.original.root_path'])
    expect(
      sites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ row }) => <PathText value={row.original.name} /> }]`,
      ),
    ).toEqual(['root_path table cell'])
    expect(
      sites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ row }) => <PathText value={row.original.root_path} /> }]`,
      ),
    ).toEqual([])
    expect(
      sites(
        `const columns = [{ accessorKey: 'name', cell: ({ getValue }) => <span>{getValue<string>()}</span> }]`,
      ),
    ).toEqual([])
  })

  it('allows a ternary guard in a path-backed table cell', () => {
    expect(
      rawPathSites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ getValue }) => getValue<string>() ? <PathText value={getValue<string>()} /> : '-' }]`,
      ),
    ).toEqual([])
  })

  it('allows an && guard in a path-backed table cell', () => {
    expect(
      rawPathSites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ getValue }) => getValue<string>() && <PathText value={getValue<string>()} /> }]`,
      ),
    ).toEqual([])
  })

  it('checks the left side of || in a path-backed table cell', () => {
    expect(
      rawPathSites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ getValue }) => getValue<string>() || <PathText value={getValue<string>()} /> }]`,
      ).map((s) => s.expr),
    ).toEqual(['root_path table cell'])
  })

  it('rejects a raw path in another component of a table cell', () => {
    expect(
      rawPathSites(
        `const columns = [{ accessorKey: 'root_path', cell: ({ row }) => <><PathText value={row.original.root_path} /><Mono value={row.original.root_path} /></> }]`,
      ).map((s) => s.expr),
    ).toEqual(['root_path table cell'])
  })

  it('shows values in full where a person reads them before acting', () => {
    const expected = [
      ['features/agentops/workspace-file-viewer.tsx', 'path', 2],
      ['features/agentops/workspaces-panel.tsx', 'browse.root_path', 1],
      ['features/workspace-dashboard/session-folders.tsx', 'wiki.root_path', 1],
    ] as const
    for (const [file, value, count] of expected) {
      const calls = pathTextCalls(readFileSync(resolve(SRC, file), 'utf8'))
      expect(
        calls.filter((call) => call.value === value && call.wrap),
        file,
      ).toHaveLength(count)
    }
    expect(
      pathTextCalls(`const A = () => <PathText value={path} wrap={false} />`),
    ).toEqual([{ value: 'path', wrap: false }])
    expect(
      pathTextCalls(`const A = () => <PathText value={path} wrap={true} />`),
    ).toEqual([{ value: 'path', wrap: true }])
  })

  it('scans a real number of files, so an empty sweep cannot read as clean', () => {
    expect(sources.length).toBeGreaterThan(400)
  })
})

// A SECOND CUTTER (#3135, review round 3). A helper that cuts a path into a display string
// before render (`${head}/…/${tail}`, a split at the last slash) hides the whole value from
// every page that shows the string, and the checks above cannot see it: the field is a summary
// and the cut is in a `.ts` file. `path-split.ts` is the one splitter PathText uses; any other
// split of a value at its last slash is declared here with its reason.
const CUT_SOURCES = walk(SRC, ['.ts', '.tsx']).filter(
  (f) => !/\.test\.tsx?$/.test(f) && !/\.gen\.ts$/.test(f),
)
const SPLITTER = 'components/ui/path-split.ts'
const DECLARED_SPLITS: ReadonlyArray<{ file: string; reason: string }> = [
  {
    file: 'features/console/secret-references.ts',
    reason:
      "a vault secret's folder (prod/db of prod/db/password): a name, not a filesystem path, and never shown cut",
  },
]

/** The lines where code (not a comment) builds a path around an ellipsis or splits a value
 * at its last slash. */
function pathCuts(text: string): number[] {
  const src = ts.createSourceFile(
    'x.tsx',
    text,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  )
  const lines: number[] = []
  const at = (n: ts.Node) =>
    src.getLineAndCharacterOfPosition(n.getStart(src)).line + 1
  const visit = (n: ts.Node) => {
    if (
      (ts.isStringLiteral(n) ||
        ts.isNoSubstitutionTemplateLiteral(n) ||
        ts.isTemplateHead(n) ||
        ts.isTemplateMiddle(n) ||
        ts.isTemplateTail(n)) &&
      /\/…|…\//.test(n.text)
    )
      lines.push(at(n))
    if (
      ts.isCallExpression(n) &&
      ts.isPropertyAccessExpression(n.expression) &&
      n.expression.name.text === 'lastIndexOf' &&
      n.arguments[0] !== undefined &&
      ts.isStringLiteralLike(n.arguments[0]) &&
      (n.arguments[0].text === '/' || n.arguments[0].text === '\\')
    )
      lines.push(at(n))
    ts.forEachChild(n, visit)
  }
  visit(src)
  return lines
}

describe('no second cutter turns a path into a display string', () => {
  it('cuts paths only in the shared splitter, in .ts and .tsx alike', () => {
    const found: string[] = []
    for (const file of CUT_SOURCES) {
      const rel = relative(SRC, file).split('\\').join('/')
      if (rel === SPLITTER || DECLARED_SPLITS.some((d) => d.file === rel))
        continue
      for (const line of pathCuts(readFileSync(file, 'utf8')))
        found.push(`${rel}:${line}`)
    }
    expect(found).toEqual([])
  })

  it('declares only splits that are there, each with its reason', () => {
    for (const d of DECLARED_SPLITS) {
      expect(d.reason.length, d.file).toBeGreaterThan(20)
      expect(
        pathCuts(readFileSync(resolve(SRC, d.file), 'utf8')).length,
        d.file,
      ).toBeGreaterThan(0)
    }
  })

  it('finds a path built around an ellipsis and a split at the last slash, not a comment', () => {
    expect(pathCuts('const cut = `${head}/…/${tail}`')).toEqual([1])
    expect(pathCuts("const a = 'src/…/x.ts'")).toEqual([1])
    expect(pathCuts("const i = value.lastIndexOf('/')")).toEqual([1])
    expect(pathCuts('// GET …/activity\nconst x = 1')).toEqual([])
  })

  it('scans .ts files as well as components', () => {
    expect(CUT_SOURCES.some((f) => f.endsWith('.ts'))).toBe(true)
    expect(CUT_SOURCES.length).toBeGreaterThan(sources.length)
  })
})
