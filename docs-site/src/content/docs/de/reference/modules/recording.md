---
title: "Aufzeichnung privilegierter Sessions"
description: >-
  Eine unveränderliche, ledger-verankerte, abspielbare Aufzeichnung dessen, was
  eine privilegierte Operator-Session tatsächlich auf den sensibelsten
  Oberflächen der Engine getan hat: append-only Frames, beim Schreiben
  maskiert, pro Session hash-verkettet und per PayloadHash in das signierte
  Audit-Ledger verankert. PAM-konform, LIVE.
---

Recording (`modules/recording`) ist die Ebene für die **Aufzeichnung
privilegierter Sessions** — die PAM-konforme Kontrolle, die Käufer mit hohen
Sicherheitsanforderungen für Konsolen und Notfallzugriff erwarten. Sie erfasst
als strukturierte Evidenz, was eine privilegierte Operator-Session tatsächlich
auf den sensibelsten Moduloberflächen getan hat, und bindet diese Evidenz an das
manipulationsevidente Audit-Ledger, sodass sie nachträglich nicht umgeschrieben
werden kann. **Reife: LIVE.**

## Was es aufzeichnet

Eine **Recording-Session** ist das privilegierte Fenster eines Credentials — die
Login-Session eines menschlichen Operators oder ein Service-Token auf der
Break-Glass-Ebene — innerhalb eines Tenants. Ihre **Frames** sind eine
append-only Spur (Unveränderlichkeitsschutz auf DB-Ebene), ein Frame pro
Modul-Routen-Aktion auf einer aufgezeichneten Oberfläche: wer, wann, die
Routen-Gestalt und Berechtigung, bereinigte Ziel-Identifikatoren, Delegation, das
Ergebnis und ein Einweg-SHA-256 des Request-Bodys. Frames sind **strukturierte
Aktionsereignisse, niemals Transkripte oder Bodies** — Parameterwerte durchlaufen
beim Schreiben eine beschränkte Maskierungsroutine, sodass ein E-Mail- oder
credential-förmiger Wert nie persistiert wird.

Die Erfassung sitzt am Modul-Routen-Wrapper der Engine und ist **deny-closed**:
auf einer aufgezeichneten Oberfläche bedeutet keine anfügbare Evidenz keine
privilegierte Aktion. Der aufgezeichnete Umfang ist jede Break-Glass-Route für
jeden Principal (die verpflichtende, nicht konfigurierbare Untergrenze) plus die
pro Tenant konfigurierten privilegierten Namespaces.

## Integrität und Replay

Die Frames jeder Session sind **hash-verkettet**, und die Kettenspitze ist per
`PayloadHash` **in das signierte Audit-Ledger verankert** — ein Open-Event beim
Session-Start, periodische Anker während des Laufs und ein Seal beim Schließen.
Das Umschreiben irgendeines Frames bricht sowohl die Session-Kette als auch ihre
versiegelten Ledger-Anker. `GET /sessions/{id}/verify` berechnet die Kette neu
und prüft jeden Anker; `GET /sessions/{id}/replay` rekonstruiert die
menschenlesbare Timeline, korreliert mit dem Ledger-Fenster der Session. Die
Oberfläche wurzelt unter `/v1/m/recording/` (`sessions`, `replay`, `verify`,
`seal`, `config`, `ack`).

## Bounded Context, klar benannt

- Es zeichnet **Modulrouten** (`/v1/m/<ns>`) auf; die Core-`/v1`-Oberflächen sind
  ledger-auditiert, aber nicht frame-aufgezeichnet — Replay korreliert sie
  stattdessen über das Ledger-Fenster der Session.
- Bei einer **aktiven** Session sind Frames nach dem letzten periodischen Anker
  nur durch die Kettenspitze gebunden, bis zum nächsten Anker oder Seal; `verify`
  meldet `anchored_through`, sodass die Grenze explizit und nie impliziert ist.
- Es implementiert **kein Purge und kein Legal-Hold** — Retention/Legal-Hold
  besitzt die Löschung; Ledger-Anker überleben jedes Purge.
- Dies ist das Recording-Subsystem, das das **agentops-Governance-Panel** für die
  I/O-Aufzeichnung pro Session nutzt: jeder gebrückte Claude-Code-Frame wird in
  dasselbe hash-verkettete, ledger-verankerte Muster eingefaltet.

## Verwandt

- [Sicherheit](/de/reference/modules/ix-security/) — die umgebende Sicherheits-
  und Datenschutzebene (Guardrails, DLP, Retention, Residency).
- [Sessions](/de/reference/modules/ii-sessions/) — beherbergt die geregelte
  Claude-Code-Session-Runtime, deren I/O pro Session dieses Subsystem aufzeichnet.
- [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) — die Posture live /
  on-demand / deny-closed über die Engine hinweg.
