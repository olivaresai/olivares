<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# CRA Article 14 runbook — authority reporting, end to end

**Status:** procedure prepared pre-market. The project is beta, not yet placed on the EU
market, so the obligation is dormant today — but Article 14 reporting applies from
**2026-09-11**, and from launch day if first EU market placement happens after that date
(which is the plan of record). This runbook exists so the first real execution is not the
first execution ever. Adjudicated against primary sources in an internal research audit of
2026-07-28 (per-claim URLs and access dates; evidence cutoff 2026-07-28). All article
references below are to
[Regulation (EU) 2024/2847](https://eur-lex.europa.eu/eli/reg/2024/2847/oj/eng).

**Division of labour** — this file is the *authority-reporting clock machinery*:

| File | Role |
|---|---|
| [`SECURITY.md`](../SECURITY.md) | Policy: intake channel, CVD, remediation targets |
| [`docs/CRA-READINESS.md`](CRA-READINESS.md) | CRA criteria, deadlines, notification templates |
| [`docs/PSIRT-RUNBOOK.md`](PSIRT-RUNBOOK.md) | Fix pipeline: out-of-band release, advisory feed, rule-packs |
| [`docs/STATUS-AND-INCIDENT-COMMS.md`](STATUS-AND-INCIDENT-COMMS.md) | User-facing incident communications |
| **This file** | Who reports what to which authority, on which clock, with which record |

> **Thesis.** The 24/72-hour CRA deadlines are elapsed UTC-hour clocks that start at
> *awareness*, not convenience. Final-report periods use separate statutory anchors and
> calendar rules: 14 days after a corrective or mitigating measure becomes available for a
> vulnerability; one month after the actual 72-hour notification for an incident. A solo
> maintainer meets those clocks only with a pre-decided procedure: the decision test is
> written down, the payloads are pre-shaped to the platform's fields, the identity (EU Login)
> exists *before* the first incident, and the drill has been run offline. Improvisation is how
> a 24-hour clock is missed.

---

## 0. Roles, triggers, and the clock

- **Incident owner / decision-maker:** the maintainer (solo founder). Every timestamp is
  recorded in UTC.
- **The two mandatory flows and their enacted triggers:**
  - **Actively exploited vulnerability** (Art 3(42), 14(1)): *reliable evidence that a
    malicious actor has exploited a vulnerability contained in the product in a system
    without the system owner's permission*. Severity scores, lab exploitability, or a CVE's
    mere existence are evidence inputs, **not** the legal test. CISA KEV listings and
    validated customer exploitation reports are strong evidence toward that test.
  - **Severe incident** (Art 14(3), 14(5)): an incident impacting product security that
    (a) negatively affects — or is *capable* of negatively affecting — the product's ability
    to protect the **availability**, authenticity, integrity, or confidentiality of
    sensitive or important data or functions; or (b) has led or is capable of leading to the
    introduction or execution of malicious code in the product or in users' systems.
    Availability is expressly included; completed compromise is not required.
- **T0 ("awareness"):** the earliest UTC instant at which, after a **prompt initial
  assessment**, the maintainer has a **reasonable degree of certainty** that one of the two
  triggers above is met (Commission guidance C(2026) 5252, Annex ¶211-215 — non-binding but
  the best official interpretation). T0 is neither every unvalidated signal nor the end of
  root-cause analysis. The raw intake/detection timestamp is recorded **separately** from T0.
- **Clock computation:** the 24/72-hour periods are elapsed UTC hours; nights, weekends,
  and holidays count. The 14-day and one-month periods use Regulation 1182/71 calendar
  rules: calculate one month by corresponding date (or the last day where that date does
  not exist), include weekends/holidays, and submit by the nominal calendar deadline rather
  than relying on a possible next-working-day extension. The clocks:

| Stage | Vulnerability flow | Incident flow |
|---|---|---|
| Early warning | **Without undue delay and in any event** ≤ **24 h** from T0 (internal target: T0+12 h) | **Without undue delay and in any event** ≤ **24 h** from T0 (internal target: T0+12 h) |
| Notification | Unless already supplied, **without undue delay and in any event** ≤ **72 h** from T0 (internal target: T0+60 h) | Unless already supplied, **without undue delay and in any event** ≤ **72 h** from T0 (internal target: T0+60 h) |
| Final report | ≤ **14 days after a corrective or mitigating measure becomes available** — record that instant; it is NOT 14 days from T0 | ≤ **1 month after the actual submission** of the 72 h notification — record the submission instant |

- **Micro-enterprise relief (Art 64(10)(a), as corrected 2025-07-02):** micro/small
  manufacturers are spared *administrative fines* only for missing the **24-hour
  early-warning deadline** — nothing else. The duty itself, "without undue delay", the 72 h
  and final reports, intermediate-report requests, user information, and corrective orders
  all apply in full. **Never operationalise the fine relief as a grace period.**

## 1. Intake → CRA decision

Every report that enters through `SECURITY.md` (`security@olivares.ai`) or a GitHub
private security advisory, and every operational incident, gets an **explicit CRA
decision** immediately:

1. Open the event record (§6) with the raw intake/detection UTC timestamp, source, and
   evidence pointer — *before* deciding anything.
2. Start the initial assessment at once. Test both triggers of §0 against the evidence.
   Record what was reviewed, the tests run, and the confidence reached.
3. Set **T0** at the earliest reasonable-certainty instant — or record a **negative** or
   **watch** decision with its basis. The negative record is what proves diligence if the
   event is re-evaluated later. A watch decision carries a re-evaluation checkpoint ≤ 24 h
   out; new evidence restarts the assessment, and reasonable certainty starts the clock.
4. If T0 is set: compute all deadlines (§0 table, UTC) into the event record and go to §2.

## 2. Reporting sequence — one filing, via the SRP

**The channel is a single electronic submission** through the ENISA **single reporting
platform (SRP)**, selecting the endpoint of the coordinator CSIRT of the manufacturer's main
establishment (Spain), simultaneously accessible to ENISA (Art 14(7), 16(1)). Do **not**
file separately with ENISA and a CSIRT; do not pre-register with the coordinator — ENISA
says manufacturer validation happens *with* the first real report and does not block
submission (SRP FAQ Q9). What exists in advance is the **EU Login** identity (§8).

1. **Without undue delay, and in any event by T0 + 24 h — early warning.** Content per the
   current ENISA field map (SRP FAQ Q16):
   notification type/level, manufacturer name, product, title; the Member States where the
   product is known to be available (if known); and, for an **incident**, whether unlawful
   or malicious acts are suspected. It is an early *warning*, not a finished analysis — send
   what is known, mark the rest as under investigation. Do not hold it for completeness.
2. **Without undue delay, and in any event by T0 + 72 h — notification** (unless already
   supplied). **Vulnerability:** general
   information about the product, general nature of the exploit and vulnerability, measures
   taken, measures users can take, and the sensitivity assessment **if available**.
   **Incident:** general information about the nature of the incident, time of detection or
   occurrence, initial assessment, measures taken, measures users can take, and the
   sensitivity assessment **if available**. Capture sensitivity at 24 h where possible and
   at 72 h whenever available; where justified, request **delayed onward dissemination**
   under Art 16(2) (conditions concretised by Delegated Regulation (EU) 2026/881) — the
   coordinator decides; a request is not a guaranteed embargo.
3. **Final report.** Vulnerability: ≤ 14 days after a corrective or mitigating measure is
   available — description with severity and impact, malicious-actor information where
   available, and the update/corrective details. Incident: ≤ 1 month after the actual 72 h
   submission — detailed severity/impact, likely threat type or root cause, and applied and
   ongoing mitigations.
4. **Intermediate reports (Art 14(6)):** the coordinator may request one at any point —
   calendar it in the event record with owner and due time, answer, and keep the receipt.
5. Every submission: record the exact payload (snapshot/hash), the SRP endpoint selected,
   attempted and successful submission UTC, receipt/case ID, and any errors.

## 3. Users are informed in parallel — Art 14(8)

**After becoming aware**, impacted users — and, where appropriate, all users — are informed
of the vulnerability or incident and, where necessary, given the corrective or mitigating
measures they can deploy (machine-readable format where appropriate). The enacted text has
no "without undue delay" wording here, but the decision must be prompt and risk-based: if
the manufacturer fails to inform in a timely manner, a **notified** coordinator CSIRT may
inform users itself where proportionate and necessary to prevent or mitigate impact. Per the Commission guidance, the communication may be **targeted** (relevant
users/customers, especially in sensitive environments) — it need not be public, and it
**never waits for the public GHSA**. Channel and cadence: `docs/STATUS-AND-INCIDENT-COMMS.md`
and, for vulnerabilities, the advisory path of `docs/PSIRT-RUNBOOK.md`.

## 4. Authority reporting vs coordinated disclosure

**Verdict (from the research adjudication): authority reporting never waits for GHSA
publication or a patch — the two tracks run in parallel and neither blocks the other.**

- Filing does not, by itself, publish anything. The mere act of notification does not
  increase the notifier's liability (Art 17(4)).
- Confidentiality is **real but qualified**: the report flows to the coordinator and ENISA;
  the coordinator normally disseminates to coordinators of Member States where the product
  is available (Art 16(2)); market-surveillance authorities receive what they need
  (Art 16(3)); ENISA may inform EU-CyCLONe for large-scale incidents (Art 17(1)).
  Art 16(4)-(5) and Art 63 impose security and confidentiality duties on every recipient,
  including for trade secrets and source code.
- For a **severe incident**, the coordinator — after consulting the manufacturer — can
  inform the public or **require the manufacturer to** where necessary to prevent or
  mitigate impact or in the public interest (Art 17(2)).
- After a fix exists, a publicly known notified vulnerability enters the EU vulnerability
  database **in agreement with the manufacturer** (Art 17(5)).
- **Early-window caveat:** Art 71 advances only Article 14 to 2026-09-11; Articles 16, 17
  and 63 otherwise carry the 2027-12-11 general date. Whether every non-Art-14 power (e.g.
  compelled publicity) operates independently in the window is UNKNOWN from the enacted
  text. Operating rule: rely on neither guaranteed secrecy nor guaranteed embargo, and get
  counsel if an authority proposes public disclosure in that interval.
- Never promise a reporter or a user that the GHSA embargo binds the coordinator, ENISA,
  MSAs, or an Art 17(2) decision.

## 5. Voluntary reporting — Art 15 (optional, future-gated)

Near misses, cyber threats affecting the product's risk profile, and third-party component
vulnerabilities that are **not** actively exploited in Olivares itself may be reported
voluntarily to a coordinator CSIRT or ENISA. Mandatory reports may be prioritised over
voluntary ones; voluntary reporting must not create obligations the reporter would not
otherwise have (Art 15(5)). The SRP's voluntary functionality is enabled only **after**
2026-09-11 on an unannounced date — treat this branch as **disabled** until the live
platform instructions allow it. (Third-party active exploitation *contained and exploited in
Olivares itself* is the mandatory §2 flow, not this one.)

## 6. The private security timeline (record format)

One append-only private record per event (kept outside the public repo). Minimum fields,
from the adjudication:

```text
event-id:              OLIVARES-SEC-YYYY-NNN        (immutable)
product/version:       release line, component, affected deployment facts
intake-utc:            raw detection/report instant   source + channel + evidence hash
assessment:            start UTC · owner · tests run · evidence considered · confidence
decision:              report-vuln | report-incident | both | negative | watch (+ basis)
t0-utc:                instant + exact facts + rationale + approver
member-states:         where the product is known available (+ source for the list)
deadlines-utc:         24h int/legal · 72h int/legal · final anchor + due date
sensitivity:           what is sensitive, why · TLP/PAP · delay request + statutory ground
submissions:           per stage: payload snapshot/hash · endpoint · attempt/success UTC · receipt/case id · errors · screenshots/logs · retry UTC
final-clock-anchor:    measure-available UTC (vuln) | 72h-submission UTC (incident)
authority-dialogue:    questions, intermediate-report requests (owner, due, answer, receipt)
users-informed:        scope (impacted/all) · rationale · content · channel · sent UTC · recipient evidence · next update
fix-and-refs:          fix/mitigation + verification evidence · GHSA / CVE / OSV / EUVD / upstream refs · embargo/publication decisions + UTC
closure:               decision · residual actions · retrospective · retention end date
```

Retention: keep the bundle **ten years after placement or the support period, whichever is
longer** — adopted as conservative internal policy aligned with Art 13(13) technical-
documentation retention; whether Art 14 artifacts legally form part of that file is UNKNOWN,
so this is labelled policy, not a quoted Art 14 requirement.

## 7. The drill — prove the clock before it runs for real

Like `docs/PSIRT-RUNBOOK.md` §7, but for the reporting path. On a cadence, and before first
EU placement:

1. Inject a dated fixture report with raw evidence (`OLIVARES-DRILL-…` namespace — never a
   real CVE, never real data).
2. Run §1 on the clock: initial assessment, T0 decision through **both** triggers, plus a
   counterfactual negative decision with its record.
3. Compute every legal and internal deadline in UTC — include a weekend crossing on purpose.
4. Fill the separate 24 h, 72 h, and final payloads for **both flows** using the current
   ENISA field map.
5. Exercise the sensitivity/delayed-dissemination wording and the targeted user notice.
6. Simulate an unavailable platform: capture mock error evidence and walk the §8 contact
   tree **without sending anything**.
7. Walk the private GHSA → fix → signed release → advisory/OSV path with repo fixtures.
8. Close with a timing record, a gap list, owner, and due dates. A drill that cannot produce
   the early warning within 24 clock-hours is a finding: fix the procedure, not the
   expectation.

**Never submit anything — fixture or real — to the live SRP, ENISA, INCIBE-CERT, or any
CSIRT during a drill.** No public manufacturer sandbox or test-submission endpoint was
identified in the reviewed official sources at the evidence cutoff; whether a non-public
testing programme can be joined is UNKNOWN, so offline payload review is the only verified
no-harm drill.

## 8. O16 — pre-launch readiness chain (dependency order)

O16 is **INCOMPLETE / EXTERNAL-DEPENDENCY**. ENISA has not yet published the live SRP
URL or coordinator list, and completion of internal steps 1-6 below is unverified.
Nothing can truthfully be marked "registered" today; this document does not claim any
checklist item is done.

1. **Routing facts:** legal manufacturer identity; Spain main-establishment basis (where the
   cybersecurity decisions are predominantly taken — Art 14(7)); EU-market product/version
   inventory; Member States where made available; founder/backup contacts.
2. **Intake real:** `security@olivares.ai` + `security.txt` monitored on the stated cadence
   (`SECURITY.md`); private timeline location ready.
3. **EU Login created and tested now** — the only onboarding step ENISA expressly permits in
   advance (SRP FAQ Q9). Store recovery/MFA material securely. Do **not** initiate SRP
   manufacturer validation before a real notification needs it.
4. **Offline artifacts:** the §2 stage payloads for both flows shaped to the current ENISA
   field map; the §6 record form; a UTC deadline calculator; sensitivity/delay-request
   block; impacted-user block; outage evidence block.
5. **Contingency contacts on file:** INCIBE-CERT current email, phones, and PGP location —
   labelled *practical private-sector escalation assumption*, NOT the statutory coordinator
   (ENISA's Spain entry said no national CVD coordinator had yet been designated, and no
   enacted Spain designation was identified in the reviewed official sources at the cutoff).
   PGP for CVD/contingency mail should exist and be tested before it is needed; it is not an
   SRP prerequisite.
6. **Drill offline** (§7), findings fixed.
7. **Watch the official dependencies:** ENISA SRP FAQ for the live URL and the Spain
   coordinator list; BOE/ministry sources for the enacted Spanish designations (only a
   draft Royal Decree was identified in the reviewed official sources at the cutoff — never
   cite it as law).
8. **Final pre-placement gate:** verify the live URL (from ENISA's page only), the Spain
   endpoint the platform presents, the live form fields, account recovery, and contacts;
   save dated evidence. If no lawful filing route exists at that gate, postponing placement
   while escalating is safer than launching with a knowingly untestable statutory process
   (risk adjudication, not a quoted requirement).

## 9. Contingencies

**Platform unreachable at a mandatory stage** — evidence preservation and escalation, never
a claim of legal equivalence (no manufacturer-side fallback or clock tolling is prescribed
anywhere in the enacted text — verified UNKNOWN):

1. T0 and every deadline stay unchanged. 2. Capture UTC, endpoint, error, correlation ID,
screenshot/log, network checks, payload hash, operator. 3. Retry from a second trusted
network without weakening endpoint verification; record every attempt. 4. Use the
helpdesk/contact the live SRP names; if Spain routing is unclear, contact INCIBE-CERT as the
labelled assumption and request current secure filing instructions in writing. 5. Send
sensitive detail off-platform only over a mechanism the competent recipient expressly
instructs; disclose the minimum needed to route. 6. Remediation and user protection continue
regardless. 7. Submit through the SRP the moment it returns; keep the receipt; explain the
outage in the report. 8. Get counsel if an outer limit is at risk.

**Maintainer absence (solo project)** — write the actual coverage, never a fictitious 24/7:

> Normal monitoring cadence: [state actual cadence]. Planned-absence coverage: [named
> competent backup with secure access and authority to assess, submit, and notify — or
> founder reachable]. Uncovered periods: [state them]. An automated alert is recorded as
> intake; T0 is set after the prompt initial assessment reaches reasonable certainty. If no
> competent cover exists, placement/availability during the absence carries a documented
> Article 14 deadline risk.

Forwarding mail to someone who cannot assess or act is not coverage. If no backup can be
arranged, the gap goes in the launch risk register and no release/holiday window is left
unmonitored — an operational risk treatment, not an enacted exemption.

## 10. Honest limits / non-claims

- No claim of SRP registration, coordinator validation, or platform access — the live SRP
  URL and the Spain coordinator list were **not published** at the evidence cutoff.
- Spain's CRA coordinator CSIRT and market-surveillance authority are **UNKNOWN** (only a
  draft Royal Decree was identified in the reviewed official sources); INCIBE-CERT is a
  labelled practical assumption.
- No binding Article 14(10) notification-format implementing act was identified in the
  official tracker or EUR-Lex search at the evidence cutoff; its status remains **UNKNOWN**.
  Payloads follow ENISA's published field map, and the **live form or any later binding act
  controls at submission**.
- No claim of EU market placement; the obligation is dormant until then.
- The voluntary branch (§5) is disabled pending official availability.
- Solo-maintainer coverage limits are stated in §9, not hidden.

## Primary sources

- Regulation (EU) 2024/2847 (Articles 3, 13-17, 63, 64, 69, 71; Annex I Part II), including
  the 2025-07-02 corrigendum:
  <https://eur-lex.europa.eu/eli/reg/2024/2847/corrigendum/2025-07-02/oj/eng>
- Delegated Regulation (EU) 2026/881 (delayed-dissemination conditions):
  <https://eur-lex.europa.eu/eli/reg_del/2026/881/oj/eng>
- ENISA, single reporting platform FAQ (status, EU Login, validation, field map):
  <https://www.enisa.europa.eu/topics/product-security-and-certification/single-reporting-platform-srp>
- Commission guidance C(2026) 5252 (non-binding; T0 ¶211-215, users ¶219-221):
  <https://ec.europa.eu/newsroom/dae/redirection/document/131456>
- Regulation (EEC, Euratom) 1182/71 (time-period rules):
  <https://eur-lex.europa.eu/eli/reg/1971/1182/1971-06-08/eng>
- Full research with per-claim URLs and access dates: internal Article 14/16 mechanics
  audit of 2026-07-28 (maintainer copy, not shipped).

## Verification and watch

Prepared and adjudicated: 2026-07-28.

- **2026-09-11:** Article 14 reporting applies (and the SRP is scheduled to be operational).
- ENISA SRP page: live URL + Spain coordinator list → re-check at O16 gate and at first
  EU-market release.
- Spanish designations (coordinator CSIRT, MSA): no enacted designation was identified in
  the reviewed official sources at the cutoff; only a draft Royal Decree was identified.
- Any Art 14(10) implementing act on report formats → templates re-verify.
