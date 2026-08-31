---
title: "Modul III — die Read/Write Access Map"
description: >-
  Eine zentrale differenzierte Fähigkeit: eine Read/Write Access Map jedes
  Herkunft→Ressource-Edges, mit dem Permitted-vs-Observed-Diff (Least-Privilege-
  Drift). Wie Edges aufgebaut, klassifiziert und vertraut werden, und die Grenzen.
---

Modul III ist die **Read/Write Access Map**: welche Herkunft (Agent, Identität,
Session) welche Ressource berührt, als read oder read-write klassifiziert, und
der **Permitted-vs-Observed-Diff**, der Least-Privilege-Drift sichtbar macht. Es
ist eine der nützlichsten und differenziertesten Fähigkeiten des Produkts — eines
der 30 Module, nicht das ganze Produkt. Diese Seite ist die Referenz dafür, was
die Map ist und wie man sie ehrlich liest.

## Der Edge

Die Map ist ein Graph aus **Edges**. Jeder Edge ist der normalisierte
Minimal-Data-Fakt `origin → resource` und trägt:

| Feld | Werte | Bedeutung |
|---|---|---|
| **mode** | `read` \| `write` \| `readwrite` \| `unknown` | die Read/Write-Klassifizierung (`unknown`, wenn sie nicht bestimmt werden kann — nie geraten) |
| **source** | `otel` \| `mcp_annotation` \| `pg_audit` \| `cloudtrail` \| `ebpf` \| `policy` \| `a2a` | welches Signal den Edge erzeugt hat |
| **confidence** | `attributed` \| `approximate` | wie fest der Zugriff an die Herkunft gebunden ist |

Edges treffen als [`edge.observed`](/de/reference/events/)-Events auf dem Event-Bus
ein, und die Engine merged sie in die persistierte `AccessEdge`-Entität — die
selbst sowohl die **Permitted**- als auch die **Observed**-Seite trägt, sodass
die Access Map eine **Sicht über das allgemeine Datenmodell** ist, kein separater
Speicher.

## Wie Edges aufgebaut werden

Modul III kreuzt zwei Pfade:

- **Kooperativer Pfad** — Agents, die OpenTelemetry (`otel`) emittieren und
  MCP-Server exponieren. In Kombination mit **nativem Store-Audit** ist dies
  hochpräzise: Postgres pgAudit (`pg_audit`) klassifiziert READ/WRITE
  wortgetreu; AWS CloudTrail (`cloudtrail`) liefert S3-`readOnly`; Warehouses
  ähnlich.
- **Nicht-kooperativer Pfad** — ein Kernel-Level-**eBPF/Tetragon-Backstop**
  (`ebpf`) erfasst `MAY_READ`/`MAY_WRITE` auf Syscall-Ebene, außerhalb der
  Kontrolle des Agents (Anti-Evasion), blind für den verschlüsselten Body.

MCP-Tool-Annotationen (`readOnlyHint`/`destructiveHint`, Quelle
`mcp_annotation`) sind ein nützliches Signal, sind aber **laut
MCP-Spezifikation nicht vertrauenswürdig** — das Produkt **erhärtet** sie und
vertraut ihnen nie allein.

Die **Permitted**-Seite (Quelle `policy`) stammt aus deklarierten Grants; die
**Observed**-Seite stammt aus den obigen Signalen.

## Permitted vs Observed (Least-Privilege-Drift)

Die definierende Sicht ist der **Diff** zwischen dem, was eine Herkunft berühren
*darf*, und dem, was sie *beim Berühren beobachtet* wird. Er macht sichtbar:

- **unerwartete Zugriffe** — eine Herkunft nutzte eine Ressource, die ihr nie
  gewährt wurde;
- **ungenutzte Grants** — eine Berechtigung, die keine Herkunft je ausübte;
- **reconciliation-pending** — ein Zugriff, den das System noch nicht fest
  zuordnen kann.

Das [Zero-to-Graph-Tutorial](/de/tutorials/zero-to-graph/) erreicht ein befülltes
Drift-Ergebnis auf dem Demo-Estate.

:::caution[Ehrliche Grenzen]
- **Identität pro Agent ist eine harte Abhängigkeit.** Audit ordnet Aktivität
  einem Credential oder einer Rolle zu, nicht inhärent einem Agent. Ein geteiltes
  Service-Konto mit einem Connection-Pool lässt die Zuordnung auf `approximate`
  zusammenfallen. Gut zu governen bedeutet, Identität pro Agent auszustellen (die
  Brücke zu Modul VI).
- **Die Abdeckung ist gestaffelt.** *Clean* bei Stores mit nativem Audit (SQL,
  Object Storage, Warehouses); *lossy* bei einigen Stores (Document/Vector);
  **passiv unmöglich zu rekonstruieren** bei anderen (z. B. Redis, SQLite, D1).
  Ein fehlender Edge ist **kein** Beweis dafür, dass ein Zugriff nicht
  stattfand, wo die Abdeckung lossy oder nicht vorhanden ist.
- **`unknown` und `approximate` werden gezeigt, nicht versteckt.** Das Produkt
  erfindet nie eine Klassifizierung oder Gewissheit, die es nicht hat.
:::

## Die Map lesen

Die Access-Map-Ergebnisse — einschließlich des Permitted-vs-Observed-Drifts —
werden von Modulrouten bedient, die in der separaten **Beta**-
[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht sind (nicht im stabilen
Core-Vertrag); ihre feldgenauen Formen liegen in den
typisierten Go/TypeScript-Schnittstellen des Produkts, und die Web-UI rendert den
Graphen und das Drift-Overlay darüber. Das Lesen des Access-Graphen ist eine
**privilegierte, mandantenbezogene, vollständig auditierte** Aktion (ab der
Editor-Rolle, nie der niedrigste Viewer) — siehe das
[Sicherheitsmodell](/de/explanation/security/security-model/) und das
[Bedrohungsmodell](/de/explanation/security/threat-model/).

## Verwandt

- [Event-Bus-Referenz](/de/reference/events/) — das `edge.observed`-Event und sein Payload.
- [Architekturüberblick](/de/explanation/architecture/overview/) — wo Modul III einzuordnen ist.
- [Governen und freigeben](/de/how-to/govern-and-approve/) — auf Drift reagieren.
