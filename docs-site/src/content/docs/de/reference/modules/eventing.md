---
title: "Eventing- & Webhook-Plattform"
description: >-
  Die integratororientierte Subscription-Oberfläche über dem Event-Bus der
  Engine: typisierte Event-Subscriptions mit signierter Webhook-Zustellung,
  dauerhafte At-least-once-Semantik, Retry/Backoff, eine Dead-Letter-Queue und
  Cursor-Replay. Sie ist die Dauerhaftigkeitsgrenze, die der In-Process-Bus
  nicht bietet.
---

Eventing (`modules/eventing`, **LIVE**) verwandelt den In-Process-Event-Bus
der Engine in eine **externe Subscription-Oberfläche**. Während der Bus selbst
At-most-once arbeitet und beim Herunterfahren verwirft, ist dieses Modul die
**Dauerhaftigkeitsgrenze**: Sobald ein Event in der Erfassungstransaktion
festgehalten ist, ist die Zustellung dauerhaft und auditierbar. Seine Routen
sind unter `/v1/m/eventing/` eingebunden.

## Was Sie abonnieren

Eine **Subscription** erfasst die gewünschten Event-Typen, einen optionalen
Quellfilter, eine Consumer-Endpunkt-URL, die Rolle, unter der ihre Zustellungen
autorisiert sind, und ein servergeneriertes HMAC-Signiergeheimnis (genau einmal
zurückgegeben, danach nur noch über die versiegelte At-rest-Schnittstelle
gehalten). Die abonnierbaren Typen stammen aus einem typisierten Katalog —
`GET /event-types` gibt jeden Typ mit seiner Stabilitätsstufe und der
Berechtigung zurück, die ihn absichert. Die Verwaltung von Subscriptions ist
privilegiert und auditiert: create/update/rotate-secret sind Write-Tier; delete,
replay, redeliver und Testzustellungen sind Admin-Tier.

## Zustellungsgarantien

Die Zustellung erfolgt **At-least-once mit Consumer-Idempotenzschlüsseln** —
Exactly-once wurde als falsches Versprechen verworfen. Jedes erfasste Event wird
zu genau einer dauerhaften Zustellungszeile pro passender Subscription, die in
derselben Transaktion eingereiht wird. Worker beanspruchen Zeilen per
optimistischer Versionierung (sicher unter HA), POSTen den signierten
Event-Envelope und bestätigen entweder (2xx) oder planen den nächsten Versuch:

- **Retry/Backoff** — 408/425/429/5xx und Netzwerkfehler werden nach einem
  Backoff-Zeitplan wiederholt; jeder andere Status ist endgültig. Redirects
  werden nie verfolgt.
- **Dead-Letter-Queue** — erschöpfte Zustellungen landen im Status `dead`; ein
  Status `denied` erfasst eine RBAC-Ablehnung pro Event.
- **Cursor-Replay** — eine pro Mandant monoton steigende Sequenz (aus einer
  Cursor-Zeile zugeteilt, nicht `max(seq)`) erlaubt das erneute Abspielen ab
  einem Punkt im dauerhaften Log, begrenzt durch das Aufbewahrungsfenster.

Jeder Versuch trägt die zeitgestempelte HMAC-SHA256-Signatur im Stripe-Stil plus
eine stabile Event-ID als Idempotenzschlüssel. Vor jedem Versuch durchläuft der
Dispatcher die vollständige Deny-by-default-RBAC+ABAC-Pipeline gegen die Rolle
der Subscription, sodass ein ausgehendes Event genau so gefiltert wird wie ein
Live-Read.

## Begrenzter Kontext, klar benannt

- Der **In-Process-Bus ist At-most-once** mit Verwerfen beim Herunterfahren;
  Dauerhaftigkeit beginnt bei der Erfassungstransaktion, nicht beim Publish.
  Events, die veröffentlicht werden, während keine aktivierte Subscription
  passt, werden nicht erfasst (speichersparsam), sodass Replay nur bis zur
  Erfassung zurückreicht.
- Die NATS-Bridge über mehrere Knoten ist ehrlicherweise **At-most-once** —
  diese Plattform ist die dauerhafte Schicht darüber, keine Garantie über den
  verteilten Bus selbst.
- Sie ist die **integratororientierte** Oberfläche; [notify](/de/reference/modules/xv-notify/)
  bleibt der betreiberorientierte Alarm-Router. Siehe
  [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) für die Konventionen
  live / on-demand / deny-by-default.

## Verwandt

- [SIEM-Forwarding](/de/reference/modules/siemforward/) — versendet versiegeltes
  Audit-Ledger und Findings an SIEM-Türme; direkt auf dieser Plattform
  aufgebaut.
- [Notify](/de/reference/modules/xv-notify/) — der betreiberorientierte
  Alarm-Router an bereitgestellte Ziele.
- [Event-Referenz](/de/reference/events/) — das Event-Vokabular, das Sie abonnieren,
  und die zugestellte Envelope-Form.
