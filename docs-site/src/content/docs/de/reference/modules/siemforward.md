---
title: "SIEM/ITSM-Forwarder"
description: >-
  Liefert das versiegelte, hash-chained Audit-Ledger und Governance-Findings an
  Ihre SIEM- und ITSM-Türme in deren nativem Dialekt — OCSF 1.8, CEF, LEEF, syslog
  oder OTLP — über die langlebige Eventing-Plattform, mit einem leader-gegateten
  Cursor-Walk und At-least-once-Zustellung. Es rendert und leitet weiter; es leitet
  Integrität niemals neu ab.
---

Der SIEM/ITSM-Forwarder (`modules/siemforward`) nimmt die Evidenz, die die
Engine bereits versiegelt, und bringt sie in den Turm, den Ihr SOC ohnehin
betreibt. Er ist **LIVE**. Er besitzt keine neue Evidenz: Er durchläuft das
manipulationserkennbare Audit-Ledger
und den Governance-Findings-Stream, formt jeden Datensatz in den nativen Dialekt
des Ziels um und übergibt ihn der [Eventing-Plattform](/de/reference/modules/eventing/)
zur langlebigen Zustellung. Integritätsfelder reisen wortgetreu mit — sie werden
während des Transports niemals neu abgeleitet.

## Was er weiterleitet und wie

Zwei Hälften arbeiten zusammen. Ein **`SinkRenderer`** (er implementiert
`eventing.SinkRenderer`) formt ein erfasstes Event in das Wire-Format des Turms um:

- `audit.recorded` — ein versiegelter Ledger-Datensatz, gerendert über `core/audit`.
- `finding.reported` — ein Governance-Finding (minimaldatenbasiert: Hash plus
  geschwärzter Auszug).
- alles andere auf dem Bus — ein formatneutrales Envelope, das ein generischer
  Collector selbst parsen kann.

Unterstützte Dialekte: **OCSF 1.8**, **CEF**, **LEEF**, **syslog**, **OTLP** und
ein strukturierter JSON-Passthrough. Der Renderer ist **deny-closed**: Eine
unbekannte Sink-Art oder ein nicht renderbares Format gibt einen Fehler zurück, und
die Engine wiederholt es und schiebt die Zustellung anschließend ins Dead-Letter —
niemals ein nicht authentifizierter oder falsch geformter Versand.

Eine **leader-gegatete Forward-Pump** treibt den Rest an. Jeder Durchlauf liest
einen Cursor pro Tenant, durchläuft das Ledger ab der nächsten Sequenz in
begrenzten Batches und reiht jeden Datensatz ein. Der Cursor rückt nur über
Datensätze hinaus vor, die erfolgreich eingereiht wurden, sodass ein Absturz oder
Neustart dort fortsetzt, wo er aufgehört hat — **At-least-once** aus dem Ledger, der
maßgeblichen Quelle. Erneut durchlaufene Datensätze werden downstream dedupliziert.

## Ziele

Wohin das Ledger geht, ist eine Eventing-**Sink-Subscription** pro Tenant, keine
Self-Service-API auf diesem Modul — es mountet keine Routen. Ziele werden
**vom Betreiber bereitgestellt**: Splunk HEC, Microsoft Sentinel (Logs Ingestion /
DCR), Datadog Logs, New Relic oder ein generischer HTTPS-Collector. Die Engine
öffnet das versiegelte Credential und besitzt den Transport; der Renderer hält
keinen State und keine Credentials, sodass eine Instanz jeden Tenant und jede Sink
bedient.

## Bounded Context, klar benannt

- Er **leitet weiter**, er speichert nicht. Ein Tenant ohne Sink-Subscription ist
  ein No-op: Nichts wird eingereiht, der Cursor rückt trotzdem vor, nichts geht
  verloren.
- Die Weiterleitung läuft aus dem Cursor-Walk, **außerhalb der Ledger-Seal-
  Transaktion** — ein Netzwerk-Write sitzt niemals im Seal-Pfad.
- Dies ist ein **Push zu Ihrem Turm**, abzugrenzen vom Read-only-Pull des
  [Posture-Exports](/de/reference/modules/posture-export/). Die turmseitige Ingestion
  liegt außerhalb des Geltungsbereichs; wir rendern in den veröffentlichten Dialekt
  und liefern.

## Verwandt

- [Eventing](/de/reference/modules/eventing/) — die langlebige Subscription-Fläche
  (Retry/Backoff, DLQ, Cursor-Replay), in die dieses Modul rendert.
- [Compliance](/de/reference/modules/xiii-compliance/) — das versiegelte,
  ledger-abgeleitete Evidenzpaket, das dieser Stream ergänzt.
- [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) — der
  File-Tail-Pfad, wenn Sie keine native Sink bereitstellen können.
- [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) — was „At-least-once“ und
  „vom Betreiber bereitgestellt“ für diese Fläche bedeuten.
