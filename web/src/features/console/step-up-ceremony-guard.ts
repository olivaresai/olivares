// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Local TypeScript AST inspection of AAL3 step-up branches. The fourth cell of
// `step-up-behind-a-stale-pregate.test.tsx` consumes this: every positive
// `isStepUpRequired` property access in a subject must be classified and
// checked. Comments and string literals are not accesses. An unrecognized form
// fails with a reason; it is never skipped.
import * as ts from 'typescript'

export const STEP_UP_LIT = 'stepUp'

export type CeremonyKind = 'imperative' | 'derived-jsx' | 'classifier'

export interface CeremonyReference {
  line: number
  kind: CeremonyKind | 'unknown'
  ok: boolean
  reason: string
}

export interface CeremonyInspection {
  fileName: string
  references: CeremonyReference[]
  failures: CeremonyReference[]
}

const MUTATION_HOOKS = new Set(['useMutation', 'usePrivilegedMutation'])

function isRuntimeFunction(
  n: ts.Node,
): n is
  | ts.FunctionDeclaration
  | ts.FunctionExpression
  | ts.ArrowFunction
  | ts.MethodDeclaration
  | ts.ConstructorDeclaration {
  return (
    ts.isFunctionDeclaration(n) ||
    ts.isFunctionExpression(n) ||
    ts.isArrowFunction(n) ||
    ts.isMethodDeclaration(n) ||
    ts.isConstructorDeclaration(n)
  )
}

function skipParens(n: ts.Node): ts.Node {
  let cur = n
  while (ts.isParenthesizedExpression(cur)) cur = cur.expression
  return cur
}

function lineOf(sf: ts.SourceFile, n: ts.Node): number {
  return sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1
}

function isInside(outer: ts.Node, inner: ts.Node): boolean {
  let cur: ts.Node | undefined = inner
  while (cur) {
    if (cur === outer) return true
    cur = cur.parent
  }
  return false
}

function enclosingFunction(n: ts.Node): ts.Node | undefined {
  let cur: ts.Node | undefined = n.parent
  while (cur) {
    if (isRuntimeFunction(cur)) return cur
    cur = cur.parent
  }
  return undefined
}

function inCatchClause(n: ts.Node): boolean {
  let cur: ts.Node | undefined = n.parent
  while (cur && !isRuntimeFunction(cur)) {
    if (ts.isCatchClause(cur)) return true
    cur = cur.parent
  }
  return false
}

function isOnErrorFunction(fn: ts.Node): boolean {
  if (
    ts.isMethodDeclaration(fn) &&
    fn.name &&
    ts.isIdentifier(fn.name) &&
    fn.name.text === 'onError'
  ) {
    return true
  }
  const p = fn.parent
  return !!(
    p &&
    (ts.isPropertyAssignment(p) || ts.isPropertyDeclaration(p)) &&
    p.name &&
    ts.isIdentifier(p.name) &&
    p.name.text === 'onError'
  )
}

function inOnErrorHandler(n: ts.Node): boolean {
  const fn = enclosingFunction(n)
  return !!fn && isOnErrorFunction(fn)
}

/** Block of the catch or onError that owns `n`, not a nested callback. */
function admittedHandlerBody(n: ts.Node): ts.Block | undefined {
  const fn = enclosingFunction(n)
  if (
    fn &&
    isRuntimeFunction(fn) &&
    isOnErrorFunction(fn) &&
    fn.body &&
    ts.isBlock(fn.body)
  ) {
    return fn.body
  }
  let cur: ts.Node | undefined = n.parent
  while (cur && !isRuntimeFunction(cur)) {
    if (ts.isCatchClause(cur) && ts.isBlock(cur.block)) return cur.block
    cur = cur.parent
  }
  return undefined
}

/** True when this access sits under a `!` (possibly around &&/||). */
function isNegatedAccess(access: ts.PropertyAccessExpression): boolean {
  let cur: ts.Node = access
  while (cur.parent) {
    const p = cur.parent
    if (ts.isParenthesizedExpression(p)) {
      cur = p
      continue
    }
    if (
      ts.isPrefixUnaryExpression(p) &&
      p.operator === ts.SyntaxKind.ExclamationToken
    ) {
      return true
    }
    if (
      ts.isBinaryExpression(p) &&
      (p.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken ||
        p.operatorToken.kind === ts.SyntaxKind.BarBarToken)
    ) {
      cur = p
      continue
    }
    if (ts.isBinaryExpression(p)) {
      const eq =
        p.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken ||
        p.operatorToken.kind === ts.SyntaxKind.EqualsEqualsToken
      const neq =
        p.operatorToken.kind === ts.SyntaxKind.ExclamationEqualsEqualsToken ||
        p.operatorToken.kind === ts.SyntaxKind.ExclamationEqualsToken
      const other =
        p.left === cur ? p.right : p.right === cur ? p.left : undefined
      if (
        eq &&
        other &&
        skipParens(other).kind === ts.SyntaxKind.FalseKeyword
      ) {
        return true
      }
      if (
        neq &&
        other &&
        skipParens(other).kind === ts.SyntaxKind.TrueKeyword
      ) {
        return true
      }
    }
    break
  }
  return false
}

function lookupNameInStatement(
  stmt: ts.Statement,
  name: string,
): ts.Identifier | undefined {
  if (ts.isFunctionDeclaration(stmt) && stmt.name?.text === name) {
    return stmt.name
  }
  if (ts.isVariableStatement(stmt)) {
    for (const d of stmt.declarationList.declarations) {
      if (ts.isIdentifier(d.name) && d.name.text === name) return d.name
    }
  }
  if (ts.isClassDeclaration(stmt) && stmt.name?.text === name) return stmt.name
  return undefined
}

/**
 * Lexical binding of `id`. Function declarations in a block are visible
 * throughout the block; `const`/`let` only after their statement, and not
 * inside their own initializer.
 */
function resolveIdentifier(id: ts.Identifier): ts.Identifier | undefined {
  const name = id.text
  let from: ts.Node = id
  let scope: ts.Node | undefined = id.parent
  while (scope) {
    if (isRuntimeFunction(scope)) {
      for (const p of scope.parameters) {
        if (ts.isIdentifier(p.name) && p.name.text === name) return p.name
      }
      if (
        scope.name &&
        ts.isIdentifier(scope.name) &&
        scope.name.text === name
      ) {
        return scope.name
      }
    }
    if (
      ts.isBlock(scope) ||
      ts.isSourceFile(scope) ||
      ts.isModuleBlock(scope)
    ) {
      const stmts = scope.statements
      let crossed = false
      for (const stmt of stmts) {
        if (stmt === from || isInside(stmt, from)) {
          crossed = true
          if (ts.isFunctionDeclaration(stmt) && stmt.name?.text === name) {
            return stmt.name
          }
          continue
        }
        if (crossed) {
          if (ts.isFunctionDeclaration(stmt) && stmt.name?.text === name) {
            return stmt.name
          }
          continue
        }
        const hit = lookupNameInStatement(stmt, name)
        if (hit && hit !== id) return hit
      }
    }
    if (
      ts.isCatchClause(scope) &&
      scope.variableDeclaration &&
      ts.isIdentifier(scope.variableDeclaration.name) &&
      scope.variableDeclaration.name.text === name
    ) {
      return scope.variableDeclaration.name
    }
    from = scope
    scope = scope.parent
  }
  return undefined
}

function sameBinding(a: ts.Identifier, b: ts.Identifier): boolean {
  const ra = resolveIdentifier(a) ?? a
  const rb = resolveIdentifier(b) ?? b
  return ra === rb
}

function walkSkipNestedFunctions(
  root: ts.Node,
  visit: (n: ts.Node) => void,
): void {
  const rec = (n: ts.Node) => {
    visit(n)
    n.forEachChild((c) => {
      if (c !== root && isRuntimeFunction(c)) return
      rec(c)
    })
  }
  rec(root)
}

function collectIdentifiersNamed(root: ts.Node, name: string): ts.Identifier[] {
  const out: ts.Identifier[] = []
  const rec = (n: ts.Node) => {
    if (ts.isIdentifier(n) && n.text === name) out.push(n)
    n.forEachChild(rec)
  }
  rec(root)
  return out
}

function functionNameId(fn: ts.Node): ts.Identifier | undefined {
  if (ts.isFunctionDeclaration(fn) && fn.name) return fn.name
  if (ts.isFunctionExpression(fn) && fn.name) return fn.name
  if (ts.isMethodDeclaration(fn) && fn.name && ts.isIdentifier(fn.name)) {
    return fn.name
  }
  const p = fn.parent
  if (p && ts.isVariableDeclaration(p) && ts.isIdentifier(p.name)) return p.name
  return undefined
}

function discriminantLiterals(expr: ts.Expression): string[] | undefined {
  const e = skipParens(expr)
  if (ts.isStringLiteral(e)) return [e.text]
  if (ts.isConditionalExpression(e)) {
    const a = discriminantLiterals(e.whenTrue)
    const b = discriminantLiterals(e.whenFalse)
    if (!a || !b) return undefined
    return [...a, ...b]
  }
  return undefined
}

function stringLiteralReturns(fn: ts.Node): string[] | undefined {
  if (!isRuntimeFunction(fn) || !fn.body) return undefined
  const lits: string[] = []
  let invalid = false
  if (!ts.isBlock(fn.body)) {
    return discriminantLiterals(fn.body as ts.Expression)
  }
  walkSkipNestedFunctions(fn.body, (n) => {
    if (invalid || !ts.isReturnStatement(n)) return
    if (!n.expression) {
      invalid = true
      return
    }
    const got = discriminantLiterals(n.expression)
    if (!got) {
      invalid = true
      return
    }
    lits.push(...got)
  })
  return invalid ? undefined : lits
}

function impurityOf(fn: ts.Node): string | undefined {
  if (!isRuntimeFunction(fn) || !fn.body) return 'no body'
  if (fn.modifiers?.some((m) => m.kind === ts.SyntaxKind.AsyncKeyword)) {
    return 'async'
  }
  let bad: string | undefined
  walkSkipNestedFunctions(fn.body, (n) => {
    if (bad) return
    if (ts.isAwaitExpression(n)) bad = 'await'
    else if (ts.isCallExpression(n)) bad = 'call'
    else if (ts.isNewExpression(n)) bad = 'new'
    else if (
      ts.isBinaryExpression(n) &&
      isAssignmentOperator(n.operatorToken.kind)
    ) {
      bad = 'assignment'
    } else if (ts.isDeleteExpression(n)) bad = 'delete'
    else if (
      ts.isPrefixUnaryExpression(n) &&
      (n.operator === ts.SyntaxKind.PlusPlusToken ||
        n.operator === ts.SyntaxKind.MinusMinusToken)
    ) {
      bad = 'update'
    } else if (
      ts.isPostfixUnaryExpression(n) &&
      (n.operator === ts.SyntaxKind.PlusPlusToken ||
        n.operator === ts.SyntaxKind.MinusMinusToken)
    ) {
      bad = 'update'
    }
  })
  return bad
}

function isAssignmentOperator(kind: ts.SyntaxKind): boolean {
  switch (kind) {
    case ts.SyntaxKind.EqualsToken:
    case ts.SyntaxKind.PlusEqualsToken:
    case ts.SyntaxKind.MinusEqualsToken:
    case ts.SyntaxKind.AsteriskEqualsToken:
    case ts.SyntaxKind.AsteriskAsteriskEqualsToken:
    case ts.SyntaxKind.SlashEqualsToken:
    case ts.SyntaxKind.PercentEqualsToken:
    case ts.SyntaxKind.LessThanLessThanEqualsToken:
    case ts.SyntaxKind.GreaterThanGreaterThanEqualsToken:
    case ts.SyntaxKind.GreaterThanGreaterThanGreaterThanEqualsToken:
    case ts.SyntaxKind.AmpersandEqualsToken:
    case ts.SyntaxKind.BarEqualsToken:
    case ts.SyntaxKind.CaretEqualsToken:
    case ts.SyntaxKind.AmpersandAmpersandEqualsToken:
    case ts.SyntaxKind.BarBarEqualsToken:
    case ts.SyntaxKind.QuestionQuestionEqualsToken:
      return true
    default:
      return false
  }
}

function isClassifierFunction(fn: ts.Node): boolean {
  if (inCatchClause(fn) || inOnErrorHandler(fn)) return false
  const lits = stringLiteralReturns(fn)
  if (!lits || !lits.includes(STEP_UP_LIT)) return false
  if (new Set(lits).size < 2) return false
  return impurityOf(fn) === undefined
}

function jsxTagName(
  n: ts.JsxOpeningElement | ts.JsxSelfClosingElement,
): string | undefined {
  return ts.isIdentifier(n.tagName) ? n.tagName.text : undefined
}

function isDirectStepUpState(root: ts.Node): boolean {
  const n = skipParens(root)
  if (
    ts.isJsxSelfClosingElement(n) &&
    jsxTagName(n) === 'StepUpRequiredState'
  ) {
    return true
  }
  return (
    ts.isJsxElement(n) && jsxTagName(n.openingElement) === 'StepUpRequiredState'
  )
}

function callName(call: ts.CallExpression): string | undefined {
  const e = skipParens(call.expression)
  if (ts.isIdentifier(e)) return e.text
  if (ts.isPropertyAccessExpression(e) && ts.isIdentifier(e.expression)) {
    return `${e.expression.text}.${e.name.text}`
  }
  return undefined
}

function hasToastCall(root: ts.Node): boolean {
  let found = false
  walkSkipNestedFunctions(root, (n) => {
    if (found || !ts.isCallExpression(n)) return
    const nm = callName(n)
    if (nm && nm.startsWith('toast.')) found = true
  })
  return found
}

function isReportCallStmt(stmt: ts.Statement): boolean {
  if (!ts.isExpressionStatement(stmt)) return false
  const e = skipParens(stmt.expression)
  return ts.isCallExpression(e) && callName(e) === 'report'
}

function enclosingBlockStatements(
  iff: ts.IfStatement,
): readonly ts.Statement[] | undefined {
  const p: ts.Node | undefined = iff.parent
  if (p && ts.isBlock(p)) return p.statements
  if (p && (ts.isSourceFile(p) || ts.isModuleBlock(p))) return p.statements
  return undefined
}

function toastPrecedesIf(iff: ts.IfStatement): boolean {
  const stmts = enclosingBlockStatements(iff)
  if (!stmts) return false
  for (const stmt of stmts) {
    if (stmt === iff) return false
    if (hasToastCall(stmt)) return true
  }
  return false
}

function ifWhoseConditionContains(node: ts.Node): ts.IfStatement | undefined {
  let cur: ts.Node | undefined = node
  while (cur && cur.parent) {
    const p: ts.Node = cur.parent
    if (isRuntimeFunction(p)) return undefined
    if (ts.isIfStatement(p)) {
      return isInside(p.expression, node) ? p : undefined
    }
    cur = p
  }
  return undefined
}

function trueBranchOfCondition(cond: ts.Node): ts.Node | undefined {
  const inner = skipParens(cond)
  let p: ts.Node | undefined = inner.parent
  while (p && ts.isParenthesizedExpression(p)) p = p.parent
  if (!p) return undefined
  if (ts.isConditionalExpression(p) && skipParens(p.condition) === inner) {
    return p.whenTrue
  }
  if (
    ts.isBinaryExpression(p) &&
    p.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken &&
    skipParens(p.left) === inner
  ) {
    return p.right
  }
  if (ts.isIfStatement(p) && skipParens(p.expression) === inner) {
    return p.thenStatement
  }
  return undefined
}

function stringEqLiteral(
  n: ts.Node,
): { id: ts.Identifier; lit: string; positive: boolean } | undefined {
  if (!ts.isBinaryExpression(n)) return undefined
  const op = n.operatorToken.kind
  const positive =
    op === ts.SyntaxKind.EqualsEqualsEqualsToken ||
    op === ts.SyntaxKind.EqualsEqualsToken
  const negative =
    op === ts.SyntaxKind.ExclamationEqualsEqualsToken ||
    op === ts.SyntaxKind.ExclamationEqualsToken
  if (!positive && !negative) return undefined
  const l = skipParens(n.left)
  const r = skipParens(n.right)
  if (ts.isIdentifier(l) && ts.isStringLiteral(r)) {
    return { id: l, lit: r.text, positive }
  }
  if (ts.isIdentifier(r) && ts.isStringLiteral(l)) {
    return { id: r, lit: l.text, positive }
  }
  return undefined
}

function variableFromCall(call: ts.CallExpression): ts.Identifier | undefined {
  let p: ts.Node | undefined = call.parent
  while (p && ts.isParenthesizedExpression(p)) p = p.parent
  if (p && ts.isVariableDeclaration(p) && ts.isIdentifier(p.name)) return p.name
  return undefined
}

function mutationReceiver(
  access: ts.PropertyAccessExpression,
): ts.Identifier | undefined {
  const recv = skipParens(access.expression)
  if (!ts.isPropertyAccessExpression(recv) || recv.name.text !== 'error') {
    return undefined
  }
  const obj = skipParens(recv.expression)
  return ts.isIdentifier(obj) ? obj : undefined
}

function isMutationBinding(id: ts.Identifier): boolean {
  const b = resolveIdentifier(id)
  if (!b) return false
  const decl = b.parent
  if (!ts.isVariableDeclaration(decl) || !decl.initializer) return false
  const init = skipParens(decl.initializer)
  if (!ts.isCallExpression(init)) return false
  const callee = skipParens(init.expression)
  return ts.isIdentifier(callee) && MUTATION_HOOKS.has(callee.text)
}

function checkClassifierUses(
  sf: ts.SourceFile,
  fn: ts.Node,
): string | undefined {
  const nameId = functionNameId(fn)
  if (!nameId) {
    return 'classifier has no name, so its calls cannot be checked'
  }
  const name = nameId.text
  const resultVars: ts.Identifier[] = []
  for (const id of collectIdentifiersNamed(sf, name)) {
    if (id === nameId) continue
    const bound = resolveIdentifier(id)
    if (bound !== nameId) continue
    const p = id.parent
    if (ts.isCallExpression(p) && skipParens(p.expression) === id) {
      const v = variableFromCall(p)
      if (!v) {
        return `call of ${name} is not bound to a variable`
      }
      resultVars.push(v)
      continue
    }
    return `unrecognized reference to classifier ${name} (alias or escape)`
  }
  if (resultVars.length === 0) {
    return `classifier ${name} is never called`
  }
  for (const v of resultVars) {
    const err = checkResultBinding(sf, v)
    if (err) return err
  }
  return undefined
}

function checkResultBinding(
  sf: ts.SourceFile,
  v: ts.Identifier,
): string | undefined {
  const fn = enclosingFunction(v)
  let sawStepUp = false
  for (const id of collectIdentifiersNamed(fn ?? sf, v.text)) {
    if (id === v) continue
    if (!sameBinding(id, v)) continue
    const p = id.parent
    const eq = p && ts.isBinaryExpression(p) ? stringEqLiteral(p) : undefined
    if (eq && eq.id === id) {
      if (!eq.positive) {
        return `${v.text} compared negatively; only === '${STEP_UP_LIT}' counts`
      }
      if (eq.lit === STEP_UP_LIT) {
        const branch = trueBranchOfCondition(p)
        if (!branch) {
          return `${v.text} === '${STEP_UP_LIT}' is not a JSX/if condition`
        }
        if (!isDirectStepUpState(branch)) {
          return `${v.text} === '${STEP_UP_LIT}' true arm is not a direct StepUpRequiredState root`
        }
        sawStepUp = true
        continue
      }
      continue
    }
    return `unrecognized use of ${v.text} at line ${lineOf(sf, id)}`
  }
  if (!sawStepUp) {
    return `${v.text} is never compared to '${STEP_UP_LIT}'`
  }
  return undefined
}

function checkPredicateUses(
  sf: ts.SourceFile,
  pred: ts.Identifier,
): string | undefined {
  const fn = enclosingFunction(pred)
  let saw = false
  for (const id of collectIdentifiersNamed(fn ?? sf, pred.text)) {
    if (id === pred) continue
    if (!sameBinding(id, pred)) continue
    const branch = trueBranchOfCondition(id)
    if (!branch) {
      return `unrecognized use of predicate ${pred.text} at line ${lineOf(sf, id)}`
    }
    if (!isDirectStepUpState(branch)) {
      return `${pred.text} true arm is not a direct StepUpRequiredState root`
    }
    saw = true
  }
  if (!saw) {
    return `predicate ${pred.text} is never used as a JSX/if condition`
  }
  return undefined
}

function thenIsDiscriminantReturn(then: ts.Statement): boolean {
  const stmt =
    ts.isBlock(then) && then.statements.length === 1 ? then.statements[0] : then
  return (
    !!stmt &&
    ts.isReturnStatement(stmt) &&
    !!stmt.expression &&
    ts.isStringLiteral(skipParens(stmt.expression))
  )
}

function checkImperative(
  access: ts.PropertyAccessExpression,
): string | undefined {
  const iff = ifWhoseConditionContains(access)
  if (!iff) {
    return 'imperative isStepUpRequired is not the condition of an if'
  }
  const body = admittedHandlerBody(iff)
  if (!body || !body.statements.some((s) => s === iff)) {
    return 'imperative if is not a direct statement of the catch or onError handler body'
  }
  const stmts = ts.isBlock(iff.thenStatement)
    ? [...iff.thenStatement.statements]
    : [iff.thenStatement]
  if (
    stmts.length === 2 &&
    ts.isReturnStatement(stmts[0]) &&
    isReportCallStmt(stmts[1])
  ) {
    return 'report after return does not run'
  }
  if (
    stmts.length !== 2 ||
    !isReportCallStmt(stmts[0]) ||
    !ts.isReturnStatement(stmts[1])
  ) {
    return 'imperative then-branch must be report followed by an unconditional return'
  }
  if (hasToastCall(iff.thenStatement)) {
    return 'imperative branch toasts instead of leaving before toast'
  }
  if (toastPrecedesIf(iff)) {
    return 'a toast is reachable before the step-up if'
  }
  return undefined
}

function classifyAccess(
  sf: ts.SourceFile,
  access: ts.PropertyAccessExpression,
): CeremonyReference {
  const line = lineOf(sf, access)
  const fn = enclosingFunction(access)

  if (inCatchClause(access) || inOnErrorHandler(access)) {
    const err = checkImperative(access)
    return {
      line,
      kind: 'imperative',
      ok: err === undefined,
      reason: err ?? 'catch/onError branch reports and returns before toast',
    }
  }

  if (fn && isClassifierFunction(fn)) {
    const err = checkClassifierUses(sf, fn)
    return {
      line,
      kind: 'classifier',
      ok: err === undefined,
      reason: err ?? 'pure classifier consumed as stepUp → StepUpRequiredState',
    }
  }

  const mut = mutationReceiver(access)
  if (mut && isMutationBinding(mut)) {
    let declName: ts.Identifier | undefined
    let cur: ts.Node | undefined = access.parent
    while (cur && !isRuntimeFunction(cur)) {
      if (ts.isVariableDeclaration(cur) && ts.isIdentifier(cur.name)) {
        declName = cur.name
        break
      }
      cur = cur.parent
    }
    if (!declName) {
      const branch = trueBranchOfCondition(access)
      if (branch && isDirectStepUpState(branch)) {
        return {
          line,
          kind: 'derived-jsx',
          ok: true,
          reason:
            'mutation.error predicate true arm is a direct StepUpRequiredState root',
        }
      }
      return {
        line,
        kind: 'derived-jsx',
        ok: false,
        reason:
          'mutation.error isStepUpRequired is not a named predicate or JSX condition',
      }
    }
    const err = checkPredicateUses(sf, declName)
    return {
      line,
      kind: 'derived-jsx',
      ok: err === undefined,
      reason:
        err ??
        'mutation predicate true arm is a direct StepUpRequiredState root',
    }
  }

  const iff = ifWhoseConditionContains(access)
  if (iff && thenIsDiscriminantReturn(iff.thenStatement)) {
    return {
      line,
      kind: 'unknown',
      ok: false,
      reason:
        'return of a discriminant is not a classifier (impure, unnamed, or not consumed as stepUp JSX)',
    }
  }
  if (iff) {
    const err = checkImperative(access)
    return {
      line,
      kind: 'imperative',
      ok: err === undefined,
      reason: err ?? 'if-branch reports and returns before toast',
    }
  }

  return {
    line,
    kind: 'unknown',
    ok: false,
    reason:
      'unrecognized isStepUpRequired form (not catch/onError, not a pure classifier, not a mutation.error JSX predicate)',
  }
}

export function inspectCeremonySource(
  fileName: string,
  source: string,
): CeremonyInspection {
  const sf = ts.createSourceFile(
    fileName,
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  )
  const accesses: ts.PropertyAccessExpression[] = []
  const visit = (n: ts.Node) => {
    if (
      ts.isPropertyAccessExpression(n) &&
      n.name.text === 'isStepUpRequired' &&
      !isNegatedAccess(n)
    ) {
      accesses.push(n)
    }
    n.forEachChild(visit)
  }
  visit(sf)

  const references: CeremonyReference[] = []
  for (const access of accesses) {
    references.push(classifyAccess(sf, access))
  }

  return {
    fileName,
    references,
    failures: references.filter((r) => !r.ok),
  }
}

export function formatInspection(insp: CeremonyInspection): string {
  if (insp.references.length === 0) {
    return `${insp.fileName}: no positive isStepUpRequired access`
  }
  return insp.references
    .map(
      (r) =>
        `${insp.fileName}:${r.line} ${r.kind} ${r.ok ? 'ok' : 'FAIL'} — ${r.reason}`,
    )
    .join('\n')
}
