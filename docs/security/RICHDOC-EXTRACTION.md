<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Rich-document extraction — threat model and honest scope

The file-content connector can ingest Office Open XML documents (DOCX, PPTX, XLSX) as
governed knowledge text. Office files are **untrusted, potentially hostile input** — a
document is data an attacker fully controls. This note states exactly what the
extractor does, what it contains, and what it deliberately does **not** do, so the
guarantee is neither over- nor under-stated.

## Design: parse hostile bytes out of process

Two properties do the load-bearing work, in this order:

1. **A memory-safe, dependency-free parser.** Extraction uses only the Go standard
   library (`archive/zip` + `encoding/xml`). No third-party document parser is
   vendored, so there is no hostile-C attack surface (the class of bug that plagues
   PDF/Office libraries). Every loop is bounded — input size, entry count, per-part
   decompressed size, a total-decompression budget, output size, and XML nesting
   depth are all capped, and decompression is bounded by a hard byte limit rather than
   trusting the zip header's declared size. A malformed, bomb-shaped, or non-OOXML
   archive is **refused** with a classified error, never allowed to exhaust memory or
   CPU and never partially ingested.

2. **Out-of-process confinement (defense in depth).** The parser does not run in the
   engine process. The engine re-execs itself as a hidden `__extract` helper under the
   same plugin confinement used for connector plugins (`plugjail`), streaming the bytes
   in over stdin and the extracted text out over stdout. The confinement's
   **environment scoping always applies**: the child inherits none of the engine's
   environment — no connector secrets, no KMS or signing keys. A dedicated non-root
   uid and cgroup memory/pid ceilings apply when the engine runs privileged, and the
   whole extraction is bounded by a wall-clock timeout.

The parser (property 1) is the primary safety boundary; the sandbox (property 2) is
defense in depth, meaningful precisely because a future parser change or an
undiscovered stdlib bug should still be contained.

## What is NOT contained — read this

- **Non-privileged engine → the sandbox is weaker.** When the engine itself runs as a
  non-root user (a common deployment), the confinement cannot drop the child to a
  dedicated uid — the helper runs at the **engine's own uid**. Environment scoping
  still applies (the child's own environment carries no secrets), but a process at the
  engine's uid could read `/proc/<engine>/environ`. In that configuration the
  isolation reduces to "the parser is memory-safe stdlib code that does not read the
  engine's memory" — which it is, but the OS boundary is not the strong one. This is
  the same honest degradation the plugin confinement model records; see
  `PLUGIN-CONFINEMENT-THREAT-MODEL.md`.
- **No network isolation, seccomp, or landlock** on the helper this release (declared
  follow-ups of the confinement launcher). The extraction path performs no network I/O
  by construction, but that is a property of the code, not an enforced boundary.
- **The engine spawns one subprocess per document.** Ingest is sequential and each
  extraction is time-bounded, so this is not an amplification vector, but a very large
  corpus of Office files makes a sync proportionally slower — an honest cost, not a
  hidden one.

## Extraction scope — what text you actually get

Text extraction is **text only**. There is **no OCR**: a document with no text layer
(e.g. a deck of scanned images) yields empty text with provenance, never invented
content.

- **DOCX** — the main document body (`word/document.xml`). Headers, footers,
  footnotes/endnotes, comments and tracked-change metadata are **not** extracted.
- **PPTX** — the text of each slide, in slide-number order. Speaker notes, slide
  masters and layouts are **not** extracted.
- **XLSX** — the workbook's shared-string text. **Numeric-only cells, formulas, and
  strings stored inline in a sheet (rather than the shared-string table) are not
  rendered.** Spreadsheet extraction is text content, not a faithful grid.
- **PDF** is detected but **not** extracted this release (a separate, vetted
  dependency decision); a PDF is a counted skip, never a garbled body.
- Legacy binary Office formats (`.doc`, `.ppt`, `.xls`) are not handled.

A document the extractor cannot handle — a disabled kind, a file over the input cap, a
zip that is not really OOXML, or a malformed one — is a **counted skip**
(`docs_skipped` in the sync result), not a failure: it never aborts the surrounding
ingest, and it is never silently dropped without being counted. A genuine
transient/operational failure (the source is unreachable) is the opposite — it still
aborts, so a source outage is never mistaken for "every document deleted".
