# Current capability measurement, 2026-09-12

Measurement of the twelve capability claims against exact current sources. This audit
records what was inspected and what was not. It does not accept any capability.

## Identities

| Symbol | Object | Meaning |
|---|---|---|
| H | Community `97f799be6c5822ec4a0af1ec30511c4c50084ae4` | Product source inspected for capability assertions |
| W | Website `459faf501325cd9fb51e3944da5df4f4577d8f73` | Website source; validators applied in read-only mode |
| E | Private `2a603663d00e4d53a97d038deb220d342b52859e` | Private composition inspected as a read-only comparison object |

H, W and E are development measurements, not release identities. The producer commit P
that stores this audit follows H and is not named inside this document.

**Observation window (real UTC clock, this process):** start `2026-09-12T03:53:53Z`, end `2026-09-12T03:55:54Z`.
**Correction revision.** The maintainer personally reviewed producer P `58e3b76c405a80e7d033e4aecf8ead3c5139b83e`
and RETURNED it on 2026-09-12 against R1-R5 and the surviving C2 combined-mechanism inference.
This document is the corrected revision. Observations added by the correction are dated and marked
as such; the original measurement window above is unchanged and was not re-run.
No capability is accepted here, and no claim state, job state or release state changed.

**Original author:** the Community documentation writer.
**First correction author:** the same writer.
**Second correction author and adjudicator:** the maintainer; the later corrections below preserve the original observation window.
**Independent reviewer of P:** a separate reviewer (RETURN, 2026-09-12). **Adjudicator:** the maintainer.
**Role of the author:** the single Community documentation writer for this cut. The maintainer reviews.

**Original author-time limit, preserved.** At the original observation window the commit object
`459faf501325cd9fb51e3944da5df4f4577d8f73` could not be resolved in the object stores this writer
read, and it was not fetched. Its tree `e204cc6e5af2fa7a8383811027abf7e183cc086d` was present and
reachable through the accepted publication head `b51d885eb77f26c391d73e5b453bb9f265d39194`, whose
tree is byte-identical, and every website reading was taken from that tree. That was an
author-time lookup limit over the stores actually consulted; the earlier wording that generalized it to all local stores was unsupported and is withdrawn.

**Correction observation, 2026-09-12 (separate from the original window).** W `459faf50…` is now
resolvable in the shared website Git store. Verified in this correction:
`git cat-file -t 459faf50…` -> `commit`; `459faf50…^{tree}` = `e204cc6e5af2fa7a8383811027abf7e183cc086d`
= `b51d885eb77f26c391d73e5b453bb9f265d39194^{tree}`, checked in the website support worktree and the shared website repository. The source measurement remains H `97f799be…`, W `459faf50…`,
E `2a603663…`. The producer commit P that stores this audit is not named inside it.

## Source inventory versus executed evidence

This audit separates two kinds of fact and never merges them.

- **Source inventory.** A file, a symbol and a Git blob observed at H. A named test is a
  source anchor. Its presence is not an execution.
- **Executed evidence.** A recorded run with a receipt. **No capability claim has one.**
  No claim-acceptance job was run during this measurement, and no test suite was executed.

All twelve claims therefore keep `state: implemented_unaccepted`, `evidence: null`,
`mutation: null` and `testsAreAcceptance: false`, and all six jobs stay `implemented: false`.

## Component checks outside the claim format, with exact scope

Some components were checked outside the claim format. They are listed here so that they are
not described as nonexistent. None of these checks is claim-format capability acceptance.

| Component | What was checked, and what it showed | What it does not cover |
|---|---|---|
| The MC1 contract core module at website `e95717815ee8271508326359678654f1cc8edc2b` | Checked with its validators; fit for website composition. | Validators only. No page wiring, no current measurement, no capability promotion. |
| Website persona pages and the MC1 core at head `b51d885e` | Build and hermetic checks; fit for an ordinary website development change. | No product RC or production release claim. |
| A private composition at head `d8869212…`, public `fcf029f7…` | A local composition gate exited 0 (`gate_exit: 0`). Nothing was received remotely (`remote_received: false`) and no complete RC exists (`complete_rc: false`). | It binds none of the six claim jobs. The wiring scope wording ("preserve fixed gate floor and membership cross-check") comes from a separate wiring record (2026-09-12T00:43:57Z), not from this check. |
| Website persona pages | 117 of 117 persona pages matched the measured title and H1. | This does not establish complete page-byte equality. Deployment identity remains unknown. No capability acceptance. |

## What was corrected in this revision

1. **False absence withdrawn (sessions).** The prior record asserted that no canonical
   `WorkItem` or `Handoff` type was declared anywhere. Both are declared at H:
   `modules/sessions/work_model.go:246` and `modules/sessions/communication_model.go:732`.
   Fifteen governed work-item routes are registered at `modules/sessions/work_api.go:30-42,51,57`.
2. **False absence withdrawn (models).** The prior record asserted zero command-tree imports
   for other provider connectors. Those imports exist at H as read-only catalog and usage
   sources. They are not generation actuators, and the corrected wording says exactly that.
3. **Bounded observation kept bounded.** The task-progress statement is recorded as a
   bounded observation over the inspected sessions files, not a repository-wide absence.
4. **Rejected inference removed.** No combined cross-session state machine is asserted or
   denied as a defect. Separate owners and an acknowledgment reader port are recorded as a
   deliberate design (`cmd/olivares/wire.go:917-921`).
5. **All seven roadmap rows bound.** Each previously empty owner and control-route array now
   names a current owner and located routes at H.
6. **Line pins corrected.** `TestLiveOperation` occupies `modules/sessions/live_test.go:120-168`;
   the next test begins at line 170. The earlier 120-166 pin truncated the test and the
   earlier claim that it overlapped the next test was arithmetically wrong.

## Limits of this measurement

- Anchors were resolved by path, Git blob and line count, and the symbols cited above were
  re-read from actual file bodies. Whole-file semantics were not re-derived.
- No test was executed. Tests needing a configured OS backend, a real managed host or a
  full install and recovery run remain not run.
- E was confirmed present as a commit object and used as a read-only comparison identity.
  No private capability behavior is asserted from it.
- Release facts are not in this manifest. The public release of `v26.8.0` is a separate
  dimension and is recorded through the release contract, never as a capability state.

## Correction of the first corrected producer

At 2026-09-12T04:35:41.685142+00:00, producer `19e7b1834fd6cfcc9fe8a1c5ba656eee4002a34c` received a second correction. The source map now names the real command-tree files, all anchor counts include the newly inspected sessions permission declaration, and the website lookup limitation is consistent throughout. Historical withdrawn sentences remain in this audit and index; public gaps describe current verification limits. The work-item acceptance job must exercise existing work/handoff controls and state the unqualified progress boundary. Its implementation state remains false. These corrections change documentation and evidence attribution only.

The private orchestration receipts retain account routing and physical checkout paths. They are omitted from this selected project audit so the website build can receive it without provider account metadata or environment paths. Author models, observation times, source identities and review scope remain recorded.
