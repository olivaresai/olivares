// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Bank for the local AST inspector: each accepted class, and the mutants the
// fourth cell must still fail. Fixtures are strings; they are never written
// onto product source.
import { describe, expect, it } from 'vitest'
import {
  inspectCeremonySource,
  type CeremonyInspection,
} from './step-up-ceremony-guard'

function inspect(src: string): CeremonyInspection {
  return inspectCeremonySource('fixture.tsx', src)
}

function kinds(insp: CeremonyInspection) {
  return insp.references.map((r) => r.kind)
}

const IMPERATIVE = `
function report(err: unknown, retry?: () => void) {}
const toast = { error: (m: string) => m }
async function test() {
  try {
    await Promise.resolve()
  } catch (err: any) {
    if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }
    toast.error('failed')
  }
}
`

const IMPERATIVE_ONERROR = `
function report(err: unknown) {}
const toast = { error: (m: string) => m }
function useMutation(opts: { onError: (err: any) => void }) { opts }
function Form() {
  useMutation({
    onError: (err) => {
      if (err.isStepUpRequired) {
        report(err)
        return
      }
      toast.error('failed')
    },
  })
  return null
}
`

const DERIVED = `
function useMutation(_x: unknown) {
  return { error: null as any }
}
function StepUpRequiredState(_p: unknown) {
  return null
}
function Card() {
  const mutation = useMutation({})
  const necesitaCeremonia =
    mutation.error && mutation.error.isStepUpRequired
  return necesitaCeremonia ? (
    <StepUpRequiredState />
  ) : (
    <p>other</p>
  )
}
`

const CLASSIFIER = `
function StepUpRequiredState(_p: unknown) {
  return null
}
function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}
function View() {
  const catalogAdmission = readAdmission({})
  const rosterAdmission = readAdmission({})
  return (
    <>
      {catalogAdmission === 'stepUp' ? (
        <StepUpRequiredState />
      ) : catalogAdmission === 'admitted' ? (
        <p>ok</p>
      ) : null}
      {rosterAdmission === 'stepUp' ? (
        <StepUpRequiredState />
      ) : null}
    </>
  )
}
`

describe('step-up ceremony AST inspector', () => {
  it('accepts an imperative catch that reports and returns before toast', () => {
    const insp = inspect(IMPERATIVE)
    expect(kinds(insp)).toEqual(['imperative'])
    expect(insp.failures).toEqual([])
  })

  it('accepts an onError handler that reports and returns before toast', () => {
    const insp = inspect(IMPERATIVE_ONERROR)
    expect(kinds(insp)).toEqual(['imperative'])
    expect(insp.failures).toEqual([])
  })

  it('accepts a mutation.error predicate whose true JSX arm mounts StepUpRequiredState', () => {
    const insp = inspect(DERIVED)
    expect(kinds(insp)).toEqual(['derived-jsx'])
    expect(insp.failures).toEqual([])
  })

  it('accepts a pure classifier consumed as stepUp → StepUpRequiredState', () => {
    const insp = inspect(CLASSIFIER)
    expect(kinds(insp)).toEqual(['classifier'])
    expect(insp.failures).toEqual([])
  })

  it('reformatting a valid form does not change the verdict', () => {
    const wrapped = IMPERATIVE.replace(
      'if (err instanceof Error && err.isStepUpRequired) {\n      report(err)\n      return\n    }',
      'if (\n      err instanceof Error &&\n      err.isStepUpRequired\n    ) {\n      report(\n        err,\n      )\n      return\n    }',
    )
    const a = inspect(IMPERATIVE)
    const b = inspect(wrapped)
    expect(a.failures).toEqual([])
    expect(b.failures).toEqual([])
    expect(kinds(b)).toEqual(kinds(a))
  })

  it('fails a catch that returns the stepUp literal without report', () => {
    const src = IMPERATIVE.replace(
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }`,
      `if (err instanceof Error && err.isStepUpRequired) {
      return 'stepUp'
    }`,
    )
    const insp = inspect(src)
    expect(insp.failures.length).toBeGreaterThan(0)
    expect(insp.failures[0]?.kind).toBe('imperative')
    expect(insp.failures[0]?.reason).toMatch(/report/)
  })

  it('fails when report is replaced by toast', () => {
    const src = IMPERATIVE.replace('report(err)', "toast.error('step-up')")
    const insp = inspect(src)
    expect(insp.failures[0]?.reason).toMatch(/report/)
  })

  it('fails when the branch reports but does not return', () => {
    const src = IMPERATIVE.replace(
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }`,
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
    }`,
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.reason).toMatch(/return/)
  })

  it('fails a return that lives in a nested function, not the branch', () => {
    const src = IMPERATIVE.replace(
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }`,
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      const inner = () => {
        return
      }
    }`,
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.reason).toMatch(/return/)
  })

  it('fails a derived predicate whose true arm is not StepUpRequiredState', () => {
    const src = DERIVED.replace(
      'return necesitaCeremonia ? (\n    <StepUpRequiredState />\n  ) : (\n    <p>other</p>\n  )',
      'return necesitaCeremonia ? (\n    <p>other</p>\n  ) : (\n    <StepUpRequiredState />\n  )',
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('derived-jsx')
    expect(insp.failures[0]?.reason).toMatch(/true arm/)
  })

  it('fails when StepUpRequiredState belongs to another predicate', () => {
    const src = `
function useMutation(_x: unknown) { return { error: null as any } }
function StepUpRequiredState(_p: unknown) { return null }
function Card() {
  const mutation = useMutation({})
  const other = useMutation({})
  const necesitaCeremonia =
    mutation.error && mutation.error.isStepUpRequired
  const otherCeremonia = other.error && other.error.isStepUpRequired
  return otherCeremonia ? <StepUpRequiredState /> : <p />
}
`
    const insp = inspect(src)
    const mut = insp.failures.find((f) =>
      f.reason.includes('necesitaCeremonia'),
    )
    expect(mut, format(insp)).toBeTruthy()
  })

  it('fails an orphan classifier that is never called', () => {
    const src = `
function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}
`
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('classifier')
    expect(insp.failures[0]?.reason).toMatch(/never called/)
  })

  it('fails when one classifier consumer loses its ceremony', () => {
    const src = CLASSIFIER.replace(
      `{rosterAdmission === 'stepUp' ? (
        <StepUpRequiredState />
      ) : null}`,
      `{rosterAdmission === 'admitted' ? <p>ok</p> : null}`,
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.reason).toMatch(/rosterAdmission/)
  })

  it('does not treat isStepUpRequired in comments or strings as a reference', () => {
    const src = `
function noop() {
  // err.isStepUpRequired
  const s = 'isStepUpRequired'
  return s
}
`
    const insp = inspect(src)
    expect(insp.references).toEqual([])
  })

  it('does not accept a shadowed identifier as the classifier consumer', () => {
    const src = `
function StepUpRequiredState(_p: unknown) { return null }
function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}
function Outer() {
  const catalogAdmission = readAdmission({})
  return <Inner />
}
function Inner() {
  const catalogAdmission = 'other'
  return catalogAdmission === 'stepUp' ? <StepUpRequiredState /> : null
}
`
    const insp = inspect(src)
    expect(insp.failures[0]?.reason).toMatch(/catalogAdmission/)
  })

  it('fails a classifier that is not pure', () => {
    const src = CLASSIFIER.replace(
      `function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}`,
      `function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  console.log(query)
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}`,
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('unknown')
  })

  it('fails when the only return in the step-up arm is nested in another if', () => {
    const src = IMPERATIVE.replace(
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }`,
      `if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      if (err.transient) return
    }`,
    )
    const insp = inspect(src)
    expect(insp.failures.length).toBeGreaterThan(0)
    expect(insp.failures[0]?.kind).toBe('imperative')
    expect(insp.failures[0]?.reason).toMatch(/return/)
  })

  it('fails when a toast is reachable before the step-up if', () => {
    const src = IMPERATIVE.replace(
      `} catch (err: any) {
    if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }
    toast.error('failed')
  }`,
      `} catch (err: any) {
    toast.error('failed')
    if (err instanceof Error && err.isStepUpRequired) {
      report(err)
      return
    }
  }`,
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('imperative')
    expect(insp.failures[0]?.reason).toMatch(/toast/)
  })

  it('fails a classifier that writes with +=', () => {
    const src = CLASSIFIER.replace(
      `function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}`,
      `let observations = 0
function readAdmission(query: { error?: { isStepUpRequired?: boolean } }) {
  observations += 1
  if (query.error?.isStepUpRequired) return 'stepUp'
  return 'admitted'
}`,
    )
    const insp = inspect(src)
    expect(insp.failures.length).toBeGreaterThan(0)
    expect(insp.references.every((r) => !r.ok)).toBe(true)
  })

  it('fails a classifier true arm that only contains an unreachable StepUpRequiredState', () => {
    const src = CLASSIFIER.replace(
      `{catalogAdmission === 'stepUp' ? (
        <StepUpRequiredState />
      ) : catalogAdmission === 'admitted' ? (
        <p>ok</p>
      ) : null}`,
      `{catalogAdmission === 'stepUp' ? (
        <>{false && <StepUpRequiredState />}
          <p>still red</p>
        </>
      ) : catalogAdmission === 'admitted' ? (
        <p>ok</p>
      ) : null}`,
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('classifier')
    expect(insp.failures[0]?.reason).toMatch(/direct StepUpRequiredState/)
  })

  it('fails a derived predicate true arm that only contains an unreachable StepUpRequiredState', () => {
    const src = DERIVED.replace(
      'return necesitaCeremonia ? (\n    <StepUpRequiredState />\n  ) : (\n    <p>other</p>\n  )',
      'return necesitaCeremonia ? (\n    <>{false && <StepUpRequiredState />}<p>still red</p></>\n  ) : (\n    <p>other</p>\n  )',
    )
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('derived-jsx')
    expect(insp.failures[0]?.reason).toMatch(/direct StepUpRequiredState/)
  })

  it('fails when a handler toast precedes a step-up if nested in a later block', () => {
    const src = `
function report(err: unknown, retry?: () => void) {}
const toast = { error: (m: string) => m }
const enabled = true
async function test() {
  try {
    await Promise.resolve()
  } catch (err: any) {
    toast.error('failed')
    if (enabled) {
      if (err instanceof Error && err.isStepUpRequired) {
        report(err)
        return
      }
    }
  }
}
`
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('imperative')
    expect(insp.failures[0]?.reason).toMatch(/direct statement/)
  })

  it('fails a nested step-up if even when no toast precedes it', () => {
    const src = `
function report(err: unknown, retry?: () => void) {}
const toast = { error: (m: string) => m }
const enabled = true
async function test() {
  try {
    await Promise.resolve()
  } catch (err: any) {
    if (enabled) {
      if (err instanceof Error && err.isStepUpRequired) {
        report(err)
        return
      }
    }
    toast.error('failed')
  }
}
`
    const insp = inspect(src)
    expect(insp.failures[0]?.kind).toBe('imperative')
    expect(insp.failures[0]?.reason).toMatch(/direct statement/)
  })
})

function format(insp: CeremonyInspection): string {
  return insp.references
    .map((r) => `${r.line}:${r.kind}:${r.ok}:${r.reason}`)
    .join('\n')
}
