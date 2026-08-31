# ADR-0001: Record architecture decisions using MADR

- **Status:** accepted
- **Date:** 2026-06-07
- **Deciders:** Olivares AI
- **References:** documentation/product session establishing the ADR register

## Context and problem statement

The control plane's architecture decisions were recorded across several planning and
contract documents (architecture, stack, licensing, the per-session contracts and the
"boot decisions"). That history is real and well-segregated, but it is not in a form a
new contributor or evaluator can read decision-by-decision: *what* was decided, *why*,
and *what was rejected*. Context is lost between sessions when the rationale lives only
in long planning prose.

## Decision drivers

- A durable, decision-indexed record that survives between contributors.
- A lightweight format that does not become a documentation project of its own.
- Publishable as part of the product documentation.

## Considered options

- **MADR (Markdown Any Decision Records).** Minimal, widely adopted, Markdown-native.
- **A bespoke decision log.** More freedom, but no shared convention.
- **No formal ADRs.** Keep rationale in planning docs only.

## Decision outcome

Chosen option: **MADR**. Each already-made decision is captured as a numbered
`docs/adr/NNNN-*.md` with context, the chosen option, consequences and rejected
alternatives, and is published in the documentation site's *Explanation* section.

### Consequences

- **Good:** decisions are discoverable and self-explaining; new contributors do not
  relitigate settled questions.
- **Bad / trade-offs:** a small ongoing discipline to add a record when a decision is
  made.
- **Neutral:** existing planning docs remain the source the ADRs cite, not a thing the
  ADRs replace.

## Why the alternatives were rejected

- **Bespoke log** — reinvents a solved convention; harder for outside contributors.
- **No ADRs** — leaves rationale buried in prose, which is how context was being lost.
