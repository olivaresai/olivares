---
title: Glossar
description: >-
  Das Vokabular des Produkts, präzise: die Access Map und ihre Ehrlichkeitsachsen,
  die Beobachtungsarten, die Governance-Primitive und die operativen
  Begriffe — jeder so definiert, wie die Engine ihn tatsächlich verwendet.
---

Begriffe werden so definiert, wie die Engine sie verwendet — mehrere sind bewusst
enger als ihre Branchenverwendung, und die Enge ist der Punkt.

### Access Map (R/RW-Map)

Modul IIIs Graph von **Origins** (Agents, Identities, Sessions) und den
**Resources**, die sie berühren, jede Kante klassifiziert nach [Modus](#mode) und getaggt
mit ihrer [Signalquelle](#signal-source), [Attribution](#attribution-konfidenz)
und [Coverage-Stufe](#coverage-stufe). Eine zentrale differenzierte Fähigkeit — eines der 30
Module, nicht das ganze Produkt. Siehe [Was ist Olivares AI?](/de/start/what-is-olivares-ai/).

### Aktuierungszustände: `v1` / `on-demand` / `seam`

Die drei ehrlichen Zustände der *handelnden* Hälfte jedes Moduls. **`v1`** — live im
Standard-Binary ohne Provisioning. **`on-demand`** — gebaut und verdrahtet,
aber deny-closed oder degradiert, bis ein Operator es provisioniert (Deploy
apply/retire, Orchestration fire, Voice dispatch). **`seam`** — ein deklariertes
Interface ohne Backend. Der [Modul-Katalog](/de/reference/modules/overview/)
markiert jedes Modul; ein Regression-Guard in CI hält die Tabelle ehrlich.

### Agent

Ein AI-System (ein Coding-Agent, ein Service-Agent, ein orchestrierter Workflow-
Schritt), governt als First-Class-Entity, verschieden von der
[Identity](#identity--nhi) (Credential), als die er läuft. Das Binden von Agents an
Identities ist es, was [Attribution](#attribution-konfidenz) schärft.

### Agent-Sprawl

Der Analystenbegriff für AI-Agents, Copilots und MCP-Server, die sich über eine
Organisation ausbreiten, schneller als irgendjemand ein Inventar führt — unbekannte
Agents mit unbekanntem Zugriff. Es ist das Problem, das die
[Access Map](#access-map-rrw-map) und Discovery sichtbar machen sollen. Siehe
[Analysten-Vokabular](/de/explanation/positioning/analyst-vocabulary/).

### AI TRiSM

*AI Trust, Risk and Security Management* — ein Framework, **geprägt und besessen von
Gartner**, zur Governance des Trusts, Risikos und der Security von AI. Wir mappen unsere
Fähigkeiten auf seine **Themen** (Governance, Runtime-Inspektion, Runtime-
Enforcement, Information-Governance); wir **reproduzieren nicht** Gartners exaktes
Modell, behaupten keine Konformität und implizieren keine Befürwortung — die Taxonomie ist Gartners
proprietäre Forschung. Siehe
[Analysten-Vokabular](/de/explanation/positioning/analyst-vocabulary/).

### Approval (HITL)

Eine governte Anfrage, eine gegatete Action durchzuführen, eröffnet **deny-closed und
zeitlich begrenzt**, gebunden an den exakten Plan, entschieden von autorisierten Menschen mit
Separation-of-Duty und serverseitig durchgesetzter Expiry, und aufgezeichnet im
[Ledger](#audit-ledger). Siehe das [Rezept](/de/how-to/cookbook/hitl-approvals/).

### Attribution (Konfidenz)

Wie fest ein beobachteter Zugriff an ein *bestimmtes* Origin gebunden ist:
**`attributed`** (eine Per-Agent-Identität ist im Trail) oder
**`approximate`** (abgeleitet — ein geteilter Service-Account, ein lossy Store, ein
Kernel-Prozess, noch nicht an einen Agenten gebunden). Die Map zeigt das Level, statt
Gewissheit zu fabrizieren; die Konsole rendert attribuierte Kanten zudem als
*fest*. Das Upgraden der Attribution ist ein Identitätsproblem:
[SSO/SCIM & Identitätsquellen](/de/how-to/connectors/sso-scim-identity/).

### Audit-Ledger

Der append-only, hash-chained Record jeder Governance-Entscheidung und jedes
privilegierten Reads, geschützt durch Ed25519-Signaturen — jeder Record trägt
`seq`, `prev_hash`, `hash`, `sig`, sodass das Umschreiben der Historie kryptografisch
detektierbar ist. Er enthält niemals PII. Exponiert als Pull-Export, Push-Sink
und Offline-Verifikation (`olivares audit verify`).

### Break-Glass

Eine governte, auditierte Notfall-Elevation für *bestimmte* gegatete Actions —
bewusst **nicht** für alles verfügbar: das Wieder-Aktivieren eines
[Kill-Switches](#kill-switch) oder das Finalisieren des Lifecycles einer Identität kann niemals
break-glass-t werden.

### Checkpoint

Ein signierter Anker über die Ledger-Chain eines Tenants, geschrieben in einem Intervall
(Standard 1h). Eine **Off-Box**-Kopie des Checkpoints und des Public Keys ist
das, was die Verifikation nach einer Host-Kompromittierung angreifer-resistent macht.

### Collector

Der push-only Edge-Prozess (`olivares collector`), der
[Sources](#source) nahe den beobachteten Systemen ausführt und Beobachtungen an den
Core über gRPC pusht (optional mTLS). Collectors haben **keinen Inbound-Listener**.

### Cooperative Path

Beobachtung, die davon abhängt, dass der Agent berichtet — OTLP-Telemetrie, Hooks.
Höchste Genauigkeit wenn vorhanden, strukturell umgehbar, weshalb der
[Kernel-Backstop](#kernel-backstop) und das Store-native Audit daneben existieren.

### Coverage-Stufe

Die Genauigkeit des Signals einer *Resource*, orthogonal zur Attribution:
**clean** (natives Audit klassifiziert R/W wortgetreu — pgAudit, CloudTrail),
**lossy** (Kanten landen, aber ungenau), **opaque / impossible passively**
(keine nutzbare passive Audit-Oberfläche — das Produkt sagt das, statt zu raten);
**mixed** markiert eine Kante, die aus mehr als einer Stufe gebaut ist.

### Demo-Estate

Das synthetische Estate `serve --seed-demo` lädt durch den **realen** Event-
Bus (nur Loopback, Public-Source-Tree-Passwort, lehnt Non-Loopback-
Binds ab). Ein Lernwerkzeug, niemals ein Installationspfad.

### Destination (Output-Connector)

Die Auslieferungshälfte des Connector-Katalogs: Slack, Teams, PagerDuty,
Webhook, Splunk HEC, ServiceNow, Jira, E-Mail und Peers — sie liefern
Findings und Benachrichtigungen aus und haben keine Coverage-Stufe, weil sie nichts
beobachten.

### DR-Bundle / KEK

Das verschlüsselte, **ledger-continuity-safe** Backup, das `olivares dr backup`
erzeugt; versiegelt unter einem Key-Encryption-Key (passphrase-derived oder
KMS-provided), der separat von den Bundles reisen muss.
Siehe [Backup & Restore](/de/how-to/backup-and-restore/).

### Drift (Least-Privilege-Drift)

Der Diff zwischen [Permitted und Observed](#permitted-vs-observed): die Lücke
zwischen gewährtem und ausgeübtem Zugriff. Drei Klassen — **unexpected access**
(beobachtet, nie gewährt), **unused grant** (gewährt, nie beobachtet),
**reconciliation pending** (beobachtet, Identity-Link ungelöst).
[Triage-Rezept](/de/how-to/cookbook/drift-triage/).

### Edge / Cost / Finding

Das **geschlossene Set** der Beobachtungsarten, die eine Source emittieren kann: eine Zugriffs-
Relation, ein Usage-Kostenfakt oder ein Detective-Finding. Geschlossen per Design — ein
Connector kann keine neuen Arten erfinden, was den Minimal-Data-
Contract durchsetzbar hält.

### Estate

Alles, was du in einem Deployment governst: die Agents, Identities, MCP-
Server, Modelle, Resources und ihre Relationen, über all deine
Organisationen hinweg.

### Finding

Eine Guardrail- / Posture- / Red-Team- / Forensik-Beobachtung, die einen Hash
jedes sensiblen Details statt des Details trägt. Geroutet auf der Notification-Schiene
und zu [SIEM-Sinks](/de/how-to/cookbook/push-to-siem/).

### Guardian Agent

**Gartners** Begriff für AI, die *andere* AI-Agents überwacht oder bei ihnen interveniert.
Olivares AI liefert das **Governance-Ergebnis** der Kategorie — beobachten,
permitted-vs-observed diffen, deny-closed gaten, immutable aufzeichnen — aber als
**read-first Control Plane außerhalb des Data Paths**, nicht als inline LLM,
das Wache steht. Siehe [Analysten-Vokabular](/de/explanation/positioning/analyst-vocabulary/);
kontrastiere den In-Product-[Guardian-Loop](#guardian-loop).

### Guardian-Loop

Eine Governance-Regel, die Findings beobachtet und Containment automatisch
einsetzt — einschließlich des [Kill-Switches](#kill-switch) — wobei der
Auto-Pfad durch genau dasselbe Gate geht wie ein menschlicher Stop.

### Identity / NHI

Ein credential-tragender Principal: menschlich oder **Non-Human-Identity** (Service-
Accounts, Workload-Identities, API-Keys, Agent-Identities). Roster kommen
aus [Identitätsquellen](/de/how-to/connectors/sso-scim-identity/); ihr Binden
an Agents ist die Brücke von Beobachtung zu Governance.

### Kernel-Backstop

Der non-cooperative Beobachtungspfad: Tetragon erfasst Kernel-File-/Network-
Events außerhalb der Kontrolle des Agenten; die `ebpf`-Source konsumiert seinen Export.
Immer [`approximate`](#attribution-konfidenz), bis eine Identity den Prozess
an einen Agenten bindet. Siehe [eBPF/Tetragon](/de/how-to/connectors/ebpf-tetragon/).

### Kill-Switch

Der Estate- (oder Per-Agent-) Notfall-Stop: ein Admin-Tier-Aufruf killt jede
governte Aktuierung, fail-closed; das Wieder-Aktivieren erfordert zwei verschiedene Menschen
plus ein Post-Review, ohne Break-Glass drumherum.
[Drill-Rezept](/de/how-to/cookbook/kill-switch-drill/).

### MCP-Annotation

Ein selbst-deklarierter `readOnlyHint` / `destructiveHint` eines Servers — **untrusted gemäß
der MCP-Spezifikation**, aufgenommen nur als declared-capability-Hinweis
(`approximate`, weder beobachtet noch permitted), bestätigt und niemals
allein vertraut. Siehe [MCP-Governance](/de/how-to/connectors/mcp-governance/).

### Minimal Data

Die Wire-Level-Eigenschaft, dass Beobachtungen Identifikatoren und
Klassifizierungen tragen, niemals Payloads, SQL-Bodies, Prompts, Secrets oder PII. Eine
Eigenschaft des Connector-Vokabulars, kein Setting.

### Mode

Die Read/Write-Klassifizierung einer Kante: `read`, `write`, `readwrite` oder
`unknown` — wortgetreu aus dem Signal übernommen und **niemals abgeleitet**; `unknown`
ist eine ehrliche Antwort, keine fehlende.

### Observed / Permitted

Siehe [Permitted vs Observed](#permitted-vs-observed).

### Opaque Tokens

Die Credentials des Produkts: zufällige, widerrufbare, serverseitig validierte Tokens
(`olvs_…`-Sessions, `olvk_…`-API-Keys, `olst_…` der One-Time-Setup-Token) —
bewusst keine JWTs, sodass der Besitz eines Signing-Keys niemals Zugriff
prägen kann.

### Organization (Tenant)

Die Isolationsgrenze. Jeder Modul-Read und -Write ist tenant-scoped; auf
Postgres backstoppt Row-Level-Security es (die Engine weigert sich, als
Rolle zu laufen, die RLS umgehen könnte).

### Permitted vs Observed

Die zwei Hälften, die die Access Map diffed: **permitted**-Kanten kommen aus deklarierten
Grants und Policy; **observed**-Kanten aus Telemetrie und nativem Audit. Der
Diff ist [Drift](#drift-least-privilege-drift).

### Sealed Admission

Das deny-closed Trust-Gate für Out-of-Process-Connector-Plugins: gepinnter
Digest + Sigstore-Attestation, verifiziert gegen vom Operator gepinnte Trust-
Anchors, ohne Escape-Hatch.
Siehe [Einen Connector bauen](/de/how-to/build-a-connector/).

### Setup-Token

Der einmal verwendbare `olst_…`-Token, der bei der ersten Boot auf stdout gedruckt wird — die gesamte
Bootstrap-Credential-Story; es gibt keine Default-Credentials. Nur sein Hash
wird gespeichert.

### Signal Source

Welcher Observer eine Kante erzeugte: `pg_audit`, `cloudtrail`, `otel`, `ebpf`,
`mcp_annotation`, ein deklarierter Policy-Grant, ein A2A-Signal. Provenienz wird
niemals zusammengefasst: ein pgAudit-READ und ein MCP-Hinweis sind nicht dieselbe Evidence.

### Sink

Eine Eventing-Subscription, die Events an ein SIEM in dessen Dialekt
ausliefert (Splunk HEC, Sentinel DCR, Datadog, New Relic oder ein generischer HMAC-signierter
Webhook), in OCSF/CEF/LEEF/syslog/OTLP/JSON.
Siehe [Push zu SIEM](/de/how-to/cookbook/push-to-siem/).

### SLI / SLO

Die publizierten Service-Levels: Verfügbarkeit via `/readyz`, Request-Erfolg,
API- und Ingest-Latenz p99 — mit Single-Node- und HA-Stufen separat
und ehrlich angegeben.
Siehe [Monitoring](/de/how-to/monitor-with-prometheus/).

### Source

Ein Beobachtungs-Connector: er `Open`t mit Config, `Gather`t Beobachtungen
in den Sink der Engine und `Close`t. Engine-owned Scheduling, Minimal-Data-
Vokabular, Apache-2.0, importiert niemals den Core.
Siehe [Eine Quelle anbinden](/de/how-to/connect-a-source/).

### Stop-Gate

Der Enforcement-Check, den jede governte Aktuierung gegen den
[Kill-Switch](#kill-switch)-State macht — vor jedem anderen Gate geprüft, fail-
**closed** (das Inverse des Budget-Checks, der fail-open ist: ein kaputter
Meter darf keinen Outage verursachen, aber ein kaputter Stop-Check schon).
