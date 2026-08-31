---
title: "Governance und Freigabe (Human-in-the-Loop)"
description: "Wie ein Operator das Estate steuert: Identität und Berechtigungen, das Deny-by-Default-RBAC-Modell, die Restrict-only-Policy-Naht und die Human-in-the-Loop-Posture, bei der Entscheidungen im Audit-Ledger festgehalten werden."
---

Diese Seite ist für den Operator, der mindestens eine Quelle verbunden hat und nun das
Estate **steuern** (governance) muss: entscheiden, wer und was handeln darf, prüfen, was die Plattform
zur Oberfläche bringt, und darauf reagieren. Governance lebt in **Modul VI (Identität, Berechtigungen,
Governance)**, sitzt auf demselben Autorisierungskern wie der Rest der API und ist
**vollständig auditiert**.

:::caution[Ehrlicher Umfang: die Approval-Engine ist gebaut; die Operator-Konsole reift noch]
Was heute läuft, ist der **Autorisierungskern** — Deny-by-Default-RBAC, eine Restrict-only-
Policy-Naht, Tenant-gescopter Zugriff und ein append-only signiertes Audit-Ledger, das
jede Governance-Entscheidung und jeden privilegierten Read festhält — **plus eine funktionierende Human-in-the-Loop-
Approval-Engine**: governance-pflichtige Freigabeanfragen, gebunden an einen Plan-Hash, deny-closed und
zeitlich begrenzt eröffnet, mit **serverseitig durchgesetzter Separation-of-Duty, Duplicate-Decider- und Ablaufprüfung**,
und Approve-/Deny-Endpunkten unter dem Namespace des Governance-Moduls. Was **noch
reift**, ist die reichhaltigere **Operator-Review-Oberfläche** — eine vollständige Approval-Queue-Konsole und
strukturierte Review-UI. Diese Seite beschreibt das Modell, die Live-Endpunkte und die
Recorded-Decisions-Garantie; wo die Operator-UI noch im Design-Stadium ist, sagt sie es.
:::

## Das Autorisierungsmodell, innerhalb dessen Sie steuern

Jede Governance-Entscheidung wird von demselben Autorisierungskern getroffen, der den
Rest der Control Plane schützt. Verstehen Sie seine drei Eigenschaften, bevor Sie etwas ändern.

### RBAC ist deny-by-default

Die Autorisierung läuft **RBAC zuerst**. Ein Principal ohne Mitgliedschaft in einem Tenant wird
**verweigert** — es gibt keinen impliziten Grant. Berechtigungen sind auf einen Tenant gescopt, und der
Handler agiert nur auf dem **einzelnen Tenant, auf den die Anfrage aufgelöst wurde**, niemals auf einem, den er
neu ableitet, was Confused-Deputy- und IDOR-Klassen konstruktiv ausschließt.

Die eingebauten Rollen bilden eine Leiter zunehmender Fähigkeit:

| Rolle | Was sie kann |
|---|---|
| `viewer` | operative Daten und den Audit-Trail lesen |
| `editor` | das Obige, plus operative Daten schreiben |
| `admin` | das Obige, plus Tenant-IAM — Benutzer, Mitgliedschaften, Tokens, Einstellungen |
| `owner` | alle Berechtigungen innerhalb des Tenants |

Ein Modul deklariert seine eigenen namespaced Berechtigungen (`<namespace>:<resource>:<verb>`),
und Rollen werden diese Berechtigungen **nach Verb-Stufe** gewährt (viewer mappt auf Read, editor
auf Write, admin und owner auf Admin). Ein neues Modul führt daher eine Governance-
Oberfläche ein, ohne ein Engine-Release.

:::note[Den Access-Graph anzusehen ist eine privilegierte Aktion — beabsichtigt]
Die R/RW Access Map von Modul III ist das mit Abstand sensibelste Asset im Produkt: eine Map
dessen, was jeder Agent berühren kann, ist eine Recon-Roadmap für einen Angreifer. Daher ist **das Lesen des
Access-Graphs eine privilegierte Aktion**, gewährt ab der **Editor-Rolle aufwärts — niemals
dem niedrigsten Viewer**. Es ist **Tenant-gescopt** (ein Read kann nur den Graphen eines Tenants sehen),
und **jeder Read wird in das Audit-Ledger geschrieben** — wer wessen Zugriff angesehen hat, und
wann. Privileg, Tenant-Scoping und Self-Audit sind bewusst geschichtet; siehe das
[Sicherheitsmodell](/de/explanation/security/security-model/).
:::

### Die Policy-Naht (ABAC/PDP) schränkt nur ein

Über RBAC hinaus kann der Operator einen externen **Policy Decision Point (PDP)** für
attributbasierte Regeln verdrahten. Sie wählen die Engine mit einer einzigen Umgebungsvariablen:

```bash
# Choose one. Cedar is the embedded, pure-Go primary; OPA is an over-HTTP adapter.
OLIVARES_PDP_ENGINE=cedar   # or: opa | none
```

Beide Engines sitzen hinter einer Naht, und die Naht hat eine Invariante, die bestimmt, wie Sie
über sie nachdenken müssen:

:::tip[Der PDP kann Zugriff nur wegnehmen, niemals hinzufügen]
Die Policy-Naht komponiert als **RBAC ∩ native ABAC ∩ externer PDP**, geschnitten. Ein PDP
**schränkt nur ein; er erweitert niemals**, was RBAC bereits erlaubt hat. Sie können keine Cedar-
oder OPA-Policy verwenden, um Zugriff zu *gewähren*, den das Rollenmodell verweigert — nur um Zugriff zu verweigern, den das
Rollenmodell andernfalls erlauben würde. Dies wird durchgesetzt, es ist keine Konvention.
:::

Die zwei Adapter wahren diese Invariante auf unterschiedliche Weise, und Sie autorieren Policy
entsprechend:

- **Cedar (embedded, primär, pure-Go).** Sie schreiben `forbid`-Regeln. Eine Regel, die matcht,
  ist eine Einschränkung; ein leerer Regelsatz bedeutet, dass die RBAC-Entscheidung bestehen bleibt. Ein `permit` in Cedar
  kann die Entscheidung niemals erweitern.
- **OPA (über HTTP).** Ihr Rego muss **permit-by-default** sein (`default allow := true`,
  mit `allow := false`-Klauseln für Ihre Verweigerungen). Ein `true`-Ergebnis bedeutet keine Einschränkung;
  `false`, ein fehlendes Ergebnis oder ein beliebiger Transport- oder Non-2xx-Fehler **fällt geschlossen** — die
  Anfrage wird verweigert.

Eine **ungültige PDP-Konfiguration deaktiviert nur den externen PDP** und loggt die Tatsache —
native ABAC und RBAC steuern weiter. Eine fehlkonfigurierte Policy-Engine lässt niemals
Anfragen ungesteuert und legt die Control Plane niemals lahm. **Jede Einschränkung, die der
PDP anwendet, wird auditiert.**

## Was die Oberflächen Ihnen zum Handeln nennen

Human-in-the-Loop-Governance wird von dem getrieben, was die Plattform beobachtet und präsentiert.
Zwei Streams sagen einem Operator, was eine Entscheidung rechtfertigt:

| Stream | Modul | Was es zur Oberfläche bringt |
|---|---|---|
| **Least-Privilege-Drift** | III (Access Map) | das **Permitted-vs-Observed**-Diff — eine gewährte Fähigkeit, die auf eine Weise genutzt wird, die niemand beabsichtigt hat, oder ein Pfad, der erreichbar ist, aber nie ausgeübt wird |
| **Findings** | IX (Security, Guardrails, Forensik) | Guardrail- und Red-Team-Findings, plus der Notification-Stream, den die Plattform routet |

Modul III, die Access Map, ist **read-first** — sie beobachtet über Logs,
OpenTelemetry und (als nicht-kooperativer Kernel-Backstop) eBPF und ist **niemals im
Datenpfad des Agenten**, sodass ein Collector-Ausfall die Produktion nicht brechen kann. Sie ist außerdem
**minimal-data**: Sie speichert die Relation `agent → resource (read/write)`, niemals
Payloads, Secrets oder PII. Das Signal, das sie trägt, ist ehrlich über ihre eigene Konfidenz
(`attributed` vs `approximate`) und ihre eigene Reichweite.

:::caution[Die Abdeckung ist gestaffelt — Drift ist nicht einheitlich vollständig]
Die Genauigkeit der Access Map hängt von der Ressource ab. Die Abdeckung ist **gestaffelt**: *clean* für
SQL-Datenbanken, Object Stores und Warehouses (natives Audit klassifiziert Read vs Write
wörtlich); *lossy* für Stores wie Dokument- und Vektordatenbanken; und **passiv unmöglich
zu beobachten** für In-Memory- und embedded Stores. Steuern Sie dies im Hinterkopf: Ein
Fehlen von beobachtetem Zugriff ist kein Beweis für keinen Zugriff, wo die Abdeckung lossy oder abwesend ist.
Lesen Sie [das Threat Model](/de/explanation/security/threat-model/) dafür, was jede Stufe attestieren kann und
nicht kann.
:::

Eine Signalklasse benötigt explizites Governance-Urteil. MCP-Tool-Annotationen
(`readOnlyHint` / `destructiveHint`) sind ein nützlicher Read/Write-Hinweis, aber **durch die
MCP-Spezifikation untrusted** — Clients müssen sie als untrusted behandeln. Die Plattform
**korroboriert** sie gegen vertrauenswürdige Signale und vertraut ihnen nie allein, und so
sollten auch Sie es tun, wenn Sie auf ein Drift-Item reagieren, das nur auf einer Annotation ruht.

## Die Human-in-the-Loop-Posture

Die beabsichtigte Governance-Schleife lautet: **Oberflächen präsentieren** (Drift von Modul III, Findings
von Modul IX) → **ein autorisierter Operator entscheidet** → **die Entscheidung wird im
Audit-Ledger festgehalten**.

Alle drei Teile dieser Schleife laufen heute. **Die Oberflächen sind real** — Modul III produziert
das Permitted-vs-Observed-Diff und Modul IX produziert Findings. **Die Approval-Engine ist
real** — eine governance-pflichtige Freigabeanfrage öffnet gegen das Governance-Modul (deny-closed,
plan-hash-gebunden, zeitlich begrenzt); ein autorisierter Operator genehmigt oder lehnt über den Decision-
Endpunkt ab, und **Separation-of-Duty, Duplicate-Decider und Ablauf werden
serverseitig durchgesetzt**, sodass der Anforderer niemals seine eigene Anfrage entscheiden kann und eine abgelaufene niemals
binden kann. Und **die Aufzeichnung ist real und stark** — siehe die Garantie unten. Was
**noch im Design-Stadium** ist, ist die ausgebaute **Operator-Review-Konsole** — eine reichhaltige
Approval-Queue-UI; die Endpunkte und die Engine sind ausgeliefert, die polierte Review-Oberfläche
ist der Weg nach vorn für Modul VI.

Die Abhängigkeit, die diese Schleife glaubwürdig macht, ist **Per-Agent-Identität**. Das Audit der Plattform
schreibt Aktivität einer Credential oder Rolle zu, nicht inhärent einem Agenten; ein gemeinsamer
Service-Account mit einem Connection-Pool lässt die Zuordnung kollabieren. Gut zu steuern bedeutet daher,
**Identität pro Agent auszustellen und durchzusetzen** — die Brücke von der Beobachtung
(Modul III) zur Governance (Modul VI). Die Identitätsseite davon ist um
opake, widerrufbare First-Party-Credentials und ein Roster nicht-menschlicher Identitäten herum gebaut; das
**einzige Credential-Minting-Primitiv** im Produkt ist opt-in, attestiert, auditiert und
persistiert das geminte Token niemals. Siehe den
[Modulkatalog](/de/reference/modules/overview/) dafür, wie Identität, Berechtigungen und
Governance über das Estate hinweg komponieren.

:::tip[Die Recorded-Decisions-Garantie]
Wie tief der Workflow darüber auch sein mag, **eine Governance-Entscheidung ist eine festgehaltene
Tatsache**. Mutierende Aktionen werden mit dem **realen Akteur** in der
**gleichen Transaktion** wie die Änderung an das Audit-Ledger angehängt, und sensible Reads (der Access-Graph, das
Ledger selbst) self-auditen in einem committeten Write. Das Ledger ist **append-only,
hash-verkettet und durch Ed25519-Signaturen geschützt** — jeder Datensatz trägt
`seq`, `prev_hash`, `hash` und `sig`, sodass das Umschreiben der Historie kryptografisch
nachweisbar ist, und **es enthält niemals PII**. Sie können keine ungesteuerte Änderung vornehmen, die das
Ledger stillschweigend vergisst.
:::

### Den Datensatz out of the box herausbekommen

Für eine externe, unveränderliche Kopie — das, wonach ein Enterprise-Auditor fragt und das native
Telemetrie nicht bereitstellt — wird das Ledger als **authentifizierter Pull-Export** bereitgestellt:

```bash
# Pull the signed, hash-chained ledger for offline re-verification.
# Requires a token whose role can read the audit trail (viewer and up).
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Unterstützte `format`-Werte sind `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`, `otlp_log_record` und `ocsf` —
`otlp` liefert den vollständigen, POST-fähigen Export-Request, `otlp_envelope` ist ein exaktes Alias davon, und
`otlp_log_record` ist die reine Projektion mit einem LogRecord pro Zeile. Jeder Datensatz
trägt die Felder zur Ketten-Integrität, sodass Ihr SIEM oder WORM-Store die Kette **offline
neu verifizieren** kann. Die abgetrennte Signatur verteidigt gegen eine reine DB-Kompromittierung (Injection, ein
gestohlenes Backup oder Replica, eine RLS-umgehende Rolle) und gegen Checkpoint-Löschung; eine
**Off-Box-Kopie** ist die Kontrolle gegen einen vollständig kompromittierten Host. Siehe
[Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) für eine vollständige
File-Tail-Pipeline.

Der Least-Privilege-Drift, auf den diese Entscheidungen reagieren, ist das Permitted-vs-Observed-
Ergebnis der Access Map. Das [Zero-to-Graph-Tutorial](/de/tutorials/zero-to-graph/)
führt konkret durch das Erreichen davon auf dem Demo-Estate; die Access-Map-Modul-Oberfläche
unterliegt demselben Deny-by-Default-RBAC, Tenant-Scoping und Per-Read-Auditing wie
alles andere, weshalb das Lesen davon eine Editor-und-aufwärts-Aktion ist.

## Wohin als Nächstes

- [Sicherheitsmodell](/de/explanation/security/security-model/) — Privileg, Tenant-Scoping,
  Self-Audit und die Minimal-Data-Posture in voller Tiefe.
- [Threat Model](/de/explanation/security/threat-model/) — die Assets, Vertrauensgrenzen
  und was jede Abdeckungsstufe attestieren kann.
- [Modulkatalog](/de/reference/modules/overview/) — wie Identität, Berechtigungen und
  Governance (Modul VI) mit der Access Map (Modul III) und Findings
  (Modul IX) komponieren.
- [Eine Quelle verbinden](/de/how-to/connect-a-source/) — die Signale verdrahten, aus denen Drift und
  Findings gebaut werden.
