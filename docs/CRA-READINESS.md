<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# EU CRA readiness pack

This document is the gate documentation for the EU Cyber Resilience Act (CRA)
readiness pack for Olivares AI. It covers the reporting playbook, release-note
support-period declarations, and the secure-update statement needed before the
first EU market placement.

> **The value of this document is being honest about status and obligations.**
> Olivares AI is beta, has no tagged release, and is not yet placed on the EU
> market. This pack prepares the process; it does not claim registration,
> certification, conformity assessment, CE marking, or a release history.

## TL;DR

| Area | Current status | Operator / maintainer action |
|---|---|---|
| CRA applicability | Olivares AI is a product with digital elements. Obligations are dormant until the product is placed on the EU market. | Re-check at first monetized EU market placement. |
| Reporting regime | Article 14 reporting applies from 2026-09-11, or from launch day if the product is first placed on the EU market after that date. | T0 is set by a prompt initial assessment reaching reasonable certainty — procedure in [`CRA-ART14-RUNBOOK.md`](CRA-ART14-RUNBOOK.md) §0-1. |
| Reporting channel | **One** electronic submission via the ENISA single reporting platform, through the Spain main-establishment coordinator endpoint, simultaneously accessible to ENISA (Art 14(7), 16(1)). | O16: prepare EU Login in advance; coordinator validation happens with the first real notification — ENISA advises against any earlier registration or validation, and no live procedure is published today. |
| User notification | After becoming aware, inform impacted users and, where appropriate, all users; where necessary give corrective or mitigating measures they can deploy, in an easily processable structured machine-readable format where appropriate. If the manufacturer does not inform users in a timely manner, a notified coordinator CSIRT may do so where proportionate and necessary to prevent or mitigate impact (Art 14(8)). | Make and record a prompt, risk-based scope/content/channel decision; use the existing incident communications process. |
| Support period | No release line exists yet. The period will be determined for each substantially modified version from expected use; five years is a statutory safeguard unless expected use is shorter, not the default. | Declare each EU-market release line and its end of support at placement; re-confirm with counsel. |
| Secure updates | Signed releases, patch releases, rollback guidance, and release verification already exist. | Keep the release notes header and `scripts/verify-release.sh` path intact. |

## Applicability & role

Olivares AI is a self-hosted AI governance control plane delivered as software,
so it is treated here as a product with digital elements for CRA readiness
purposes. Today the project is beta, has no tagged release, and is not yet on
the market; CRA obligations are therefore dormant for the current repository
state.

The open-source steward versus manufacturer distinction is handled conservatively:
while source development alone is not represented here as market placement, a
monetized distribution of Olivares AI to EU users makes Olivares.AI the
manufacturer for that product line. The conclusion for this repo is therefore:
**manufacturer at monetized EU market placement**. If first EU market placement
happens on or after 2026-09-11, the Article 14 reporting regime is live from
launch day; if it happened earlier, the regime would still start on 2026-09-11.
Either way: the day this product is on the EU market on or after that date,
reporting is live.

Main CRA obligations such as conformity assessment, CE marking, and the
technical documentation file apply from 2027-12-11 and are out of scope for this
pack. They remain watch items, not current claims.

## Reporting playbook

Awareness (T0) is not a discretionary choice: it is the earliest UTC instant at which a
**prompt initial assessment** of the signal reaches a **reasonable degree of certainty**
that one of the two Article 14 triggers below is met (Commission guidance C(2026) 5252,
Annex ¶211-215). The raw intake/detection timestamp is recorded separately from T0, and a
negative or watch decision is recorded with the same rigour as a positive one. The
operational procedure — record format, deadline computation, submission steps — lives in
[`CRA-ART14-RUNBOOK.md`](CRA-ART14-RUNBOOK.md); this section keeps the decision criteria
and the pre-shaped payloads.

### Flow: actively exploited vulnerability

The controlling legal test is Article 3(42): **reliable evidence that a malicious actor
has exploited the vulnerability contained in Olivares AI in a system without the system
owner's permission**. Severity scores, lab exploitability, or a CVE's existence do not by
themselves meet it. Strong evidence inputs toward the test:

- CISA KEV listing for the vulnerability or affected component.
- Credible evidence of in-the-wild exploitation.
- Validated user or customer report showing exploitation of their deployment.

Reporting deadlines:

- Early warning: without undue delay and in any event no later than 24 hours after awareness.
- Vulnerability notification: unless already supplied, without undue delay and in any event
  no later than 72 hours after awareness.
- Final report: no later than 14 days after a corrective or mitigating measure
  is available.

### Flow: severe incident

Start this flow when an incident impacting product security meets either Article 14(5)
test: (a) it negatively affects — or is **capable** of negatively affecting — the
product's ability to protect the **availability**, authenticity, integrity, or
confidentiality of sensitive or important data or functions; or (b) it has led, or is
capable of leading, to the introduction or execution of malicious code in the product or
in a user's systems. Availability is expressly included and completed compromise is not
required; only events with no product-security dimension at all stay in the ordinary
incident process.

Reporting deadlines:

- Early warning: without undue delay and in any event no later than 24 hours after awareness.
- Incident notification: unless already supplied, without undue delay and in any event no
  later than 72 hours after awareness.
- Final report: within 1 month after the **actual submission** of the 72-hour
  notification (Art 14(4)(c)) — record that submission instant; the clock does not run
  from T0.

### Channel and prerequisite

CRA reporting is **one electronic submission** through the ENISA single reporting
platform (SRP): the manufacturer selects the endpoint of the coordinator CSIRT of its
main establishment (Spain) and the filing is simultaneously accessible to ENISA
(Art 14(7), 16(1)). There is no separate parallel filing to ENISA, and
pre-registration is not the milestone: ENISA's guidance is to prepare an **EU Login** in advance and to
initiate manufacturer validation only when a specific real notification is needed —
validation runs in parallel with the report and does not block submission. O16 is
therefore a readiness chain (EU Login, offline payloads, live-route verification at the
release gate — see [`CRA-ART14-RUNBOOK.md`](CRA-ART14-RUNBOOK.md) §8), not a
registration milestone. As of 2026-07-28 the live SRP URL and the Spain coordinator list
were **not yet published**. This document does **not** claim that O16 is done.

## Templates

Shaped to the ENISA SRP field map (SRP FAQ Q16, as consulted 2026-07-28): fields differ
by **stage** and by **flow**, CVSS is optional unless the live form requires it, and the
sensitivity assessment is captured at 24 h where possible and is obligatory-if-available
at 72 h. The live platform form controls at submission time.

### Template: early warning (≤24h)

```text
CRA early warning

Notification type:            actively exploited vulnerability | severe incident
Notification level:           early warning

Common (obligatory):
- Manufacturer / steward name and contact:
- Product name / affected version(s) / release line:  Olivares AI /
- Title of the notification:

Member States where the product is known to be made available (required if known):

INCIDENT FLOW ONLY (obligatory):
- Unlawful or malicious acts suspected:  yes / no / unknown

Optional at this stage:
- Product type/category:
- Sensitivity indication (what is sensitive and why):
- Known facts so far (short; analysis belongs to the 72h notification):
```

### Template: notification (≤72h)

```text
CRA notification (unless already supplied at 24h)

Common:
- Manufacturer / product / version identification (as at 24h, updated):
- Timeline so far (UTC):

VULNERABILITY FLOW (obligatory):
- General information about the affected product:
- General nature of the exploit and the vulnerability:
- Corrective or mitigating measures taken:
- Measures users can take:
- Sensitivity assessment (obligatory if available): what is sensitive, why,
  TLP/PAP where supported

INCIDENT FLOW (obligatory):
- General information about the nature of the incident:
- Time of detection / occurrence:
- Initial assessment:
- Corrective or mitigating measures taken:
- Measures users can take:
- Sensitivity assessment (obligatory if available):

Delayed-dissemination request (optional, reasoned — Art 16(2), DR (EU) 2026/881):
- Requested delay + justified cybersecurity ground + strictly necessary period:

Optional:
- CVSS v3.1 vector:
- Indicators of compromise / detection notes:
- Cross-references (GHSA / CVE / OSV):
```

### Template: final report (two distinct triggers)

```text
CRA final report

Clock anchor (record the exact UTC instant):
- VULNERABILITY flow: due ≤14 days after a corrective or mitigating measure
  became AVAILABLE at: ____ (not 14 days from T0)
- INCIDENT flow: due ≤1 month after the actual SUBMISSION of the 72h
  notification at: ____

Common:
- Manufacturer / product / version identification:

VULNERABILITY FLOW (obligatory):
- Description of the vulnerability, including severity and impact:
- Malicious actor information (obligatory if available):
- Details of the security update or other corrective measures available:

INCIDENT FLOW (obligatory):
- Detailed description of the incident, including severity and impact:
- Type of threat, or likely root cause:
- Corrective/mitigating measures applied and ongoing:

Supporting record (internal, retained):
- Exploitation assessment + evidence retained:
- Verification performed on the corrective measure:
- User-notification record (scope, channel, timestamps):
- Cross-references (GHSA / CVE / OSV / EUVD):
```

## Integration with the existing CVD chain

1. A report enters through [`SECURITY.md`](../SECURITY.md) and is triaged under
   the coordinated disclosure process.
2. Triage starts the prompt initial assessment at once and records it: raw intake
   timestamp, evidence considered, and the decision — positive (T0 set at the earliest
   reasonable-certainty instant), negative, or watch with a re-evaluation checkpoint.
   The 24/72-hour clocks run from T0, not from the end of analysis; the final-report
   periods run from their distinct anchors stated above.
3. Engineering follows [`PSIRT-RUNBOOK.md`](PSIRT-RUNBOOK.md): private handling, a
   `security/<id>` backport from each affected release line (or `main` while no tagged
   release exists), a patched signed release, and advisory publication.
4. The SRP filing runs in parallel with the GHSA path in
   [`security-advisories.md`](security-advisories.md) and **never waits for it**. CRA
   reporting does not replace GHSA, CVE, OSV, changelog, or release-note output — and
   the GHSA embargo does not bind the authorities: the coordinator normally disseminates
   the report onward (Art 16(2), delay only on a reasoned request the coordinator
   decides), market-surveillance authorities receive what they need (Art 16(3)), and for
   a severe incident the coordinator can require public disclosure in the public
   interest (Art 17(2)). Mark sensitivity in every submission.
   **Early-window caveat:** during 2026-09-11 through 2027-12-10, whether Articles
   16, 17, and 63 independently apply is **UNKNOWN** from the enacted transition text.
   Rely on neither guaranteed secrecy nor guaranteed embargo, and obtain counsel if an
   authority proposes compulsory publicity in that interval.
5. After becoming aware, impacted users — and where appropriate all users — are informed
   with the measures they can deploy (Art 14(8); targeted communication is acceptable,
   and it never waits for the public advisory). Use
   [`STATUS-AND-INCIDENT-COMMS.md`](STATUS-AND-INCIDENT-COMMS.md) for the user
   communications process and cadence instead of duplicating templates here.

## Support-period declarations

> **Updated 2026-07-28** against the commercial model v4
> (the internal commercial decision of 2026-07-27: term-only Commercial grants, no earned
> fallback), the internal CRA obligation audit of 2026-07-28, and the
> Commission's final guidance C(2026) 5252 of 27 July 2026, whose support-period
> chapter states the five-year minimum "operates only as a safeguard" and is "not to
> be considered as the default": the period is determined per substantially modified
> version from its expected use time.

The declared structure — re-confirm with counsel at first release:

- **Community (AGPL) edition**: public releases with public security fixes for the
  current release line; the declared support period per line follows the release
  cadence and is stated in this table when the first line is placed on the market.
- **Commercial edition (term-only, v4)**: every Commercial entitlement — runtime,
  add-ons, source exception, Rolling channel and its security support — lasts until
  the prepaid `paid_through` date (plus the 7-day involuntary-failure grace, once
  per rolling 365 days). Paid modules technically stop executing at that boundary,
  which is the factual premise of Recital 60 and Commission FAQ §4.5.2 for a support
  period tied to the active subscription. The paid-through end date (month and year)
  is displayed at the time of purchase per Article 13(19). No payment earns a
  perpetual, frozen-line or fallback right, so no frozen historical Commercial line
  requires a post-term security channel.
- Security updates issued during any support period remain retrievable for at least
  ten years from issue (Article 13(9)) without granting executable entitlement.
- Security fixes are delivered according to the SLA in [`SECURITY.md`](../SECURITY.md).

| Release line | First placed on EU market | End of support |
|---|---|---|
| — (no releases yet) | — | — |

**Condition of validity** (from the fallback re-adjudication): the per-module hard
lock at term end must exist and be regression-tested before first sale — without it
the term-tied support-period premise fails. Tracked as planned
entitlement-enforcement work, gated before first sale.

## Secure update mechanism

Security updates are distributed as signed releases, verified by
[`scripts/verify-release.sh`](../scripts/verify-release.sh), and documented as
patch releases that are separable from feature upgrades. The operator-facing
statement lives in
[`UPGRADE-AND-ROLLBACK.md`](UPGRADE-AND-ROLLBACK.md#13-security-updates--cra-statement),
including rollback guidance and the free-of-charge / without-undue-delay update
commitment.

## Out of scope / non-claims

- No conformity assessment claim.
- No CE marking claim.
- No technical documentation file claim; that 2027-12-11 obligation is future
  work.
- No ENISA reporting-platform registration claim.
- No certification claim.
- No release history or support matrix claim beyond the empty table above.

## Honesty ledger

| Claim | Status |
|---|---|
| Olivares AI is ready to evaluate CRA reporting readiness. | The pack and [`CRA-ART14-RUNBOOK.md`](CRA-ART14-RUNBOOK.md) exist; O16 remains incomplete because internal checklist completion is unverified and the official live route remains an external dependency. |
| Article 14 reporting applies today to this unreleased beta repo. | Not claimed. Obligations are dormant until EU market placement. |
| First EU market placement after 2026-09-11 must be treated as reporting-live from launch day. | Claimed as the conservative launch rule for this project. |
| ENISA platform / coordinator CSIRT registration is complete. | Not claimed. Prepare EU Login in advance; manufacturer validation begins with a specific real notification. O16 remains INCOMPLETE until internal readiness evidence is recorded and the official live route is verified. |
| Support period for future EU-market major release lines defaults to 5 years. | Not claimed; each substantially modified version's period will be declared from expected use, subject to the statutory five-year safeguard and shorter-expected-use exception. |

## Primary sources

- European Commission, Cyber Resilience Act policy page:
  <https://digital-strategy.ec.europa.eu/en/policies/cyber-resilience-act>
- Regulation (EU) 2024/2847, EUR-Lex CELEX 32024R2847:
  <https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32024R2847>
- Commission services, FAQs on the Cyber Resilience Act (non-binding; v1.3 of
  2026-07-01 as consulted 2026-07-27) — §4.5.2 subscription-tied support period,
  §4.2.5 tailor-made carve-out, §4.3.2 latest-version escape:
  <https://ec.europa.eu/newsroom/dae/redirection/document/122331>
- Commission guidance C(2026) 5252 final of 27 July 2026 (non-binding; support-period
  chapter: five years as safeguard not default, period per substantially modified
  version). Consulted 2026-07-27; full adjudication for this product in the internal CRA
  obligation audit of 2026-07-28 (maintainer copy, not shipped).
- Enforcement machinery facts (designated authorities register, Article 54
  corrective-first sequence, Article 64(10) narrow micro/small relief): recorded with
  access dates in the same audit and its contrast panel.
- ENISA, single reporting platform FAQ (platform status, EU Login, validation, field
  map; consulted 2026-07-28):
  <https://www.enisa.europa.eu/topics/product-security-and-certification/single-reporting-platform-srp>
- Commission Delegated Regulation (EU) 2026/881 (delayed-dissemination conditions):
  <https://eur-lex.europa.eu/eli/reg_del/2026/881/oj/eng>
- Regulation (EEC, Euratom) 1182/71 (time-period rules — hour clocks include weekends
  and holidays): <https://eur-lex.europa.eu/eli/reg/1971/1182/1971-06-08/eng>
- Full Article 14/16 mechanics research with per-claim URLs and access dates: internal
  audit of 2026-07-28 (maintainer copy, not shipped).

CRA article references used here include Articles 13, 14, 16, 17, 54, 64, 69, and 71.

## Verification and watch

Last re-verification: 2026-07-28 (Article 14/16 mechanics adjudicated against primary
sources — see the research audit above)

Watch items:

- ENISA SRP page: publication of the live platform URL and the Member-State coordinator
  CSIRT list (both pending at the cutoff).
- Spain's enacted CRA designations (coordinator CSIRT, market-surveillance authority) —
  only a draft Royal Decree was identified in the reviewed official sources at the cutoff;
  never cite the draft as law.
- Any Article 14(10) implementing act on notification formats → re-verify templates.
- 2026-09-11: Article 14 reporting obligations apply.
- 2027-12-11: conformity assessment, CE marking, and technical documentation
  obligations apply.
