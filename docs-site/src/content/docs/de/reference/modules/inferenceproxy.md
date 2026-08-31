---
title: "Inline-Inferenz-Proxy (PEP für /v1/messages)"
description: >-
  Ein optionaler, ausdrücklich zu aktivierender Policy-Enforcement-Point, der
  den Claude-Vertrag /v1/messages für rohe SDK- und curl-Aufrufer vorschaltet
  und Residenz, Modellzugriff, Kontextfenster, DLP und Budget in-band anwendet,
  bevor er weiterleitet — und so den ANTHROPIC_BASE_URL-Bypass schließt — mit
  manipulationserkennender Aufzeichnung standardmäßig VOR der Weiterleitung, deren
  Ausnahmen auf dieser Seite stehen statt entdeckt zu werden. Konfiguration und
  DLP-Autorierung sind live; der Listener bleibt ungemountet, bis ein Betreiber
  ihn bereitstellt.
---

Der Inline-Inferenz-Proxy ist der
Enforcement-Point für Inferenzverkehr, der **nicht** Claude Code ist — rohe
SDK- und `curl`-Aufrufer, die direkt auf den Claude-Vertrag `/v1/messages`
zugreifen. Die Entscheidung wird am Composition Root
(`cmd/olivares/inferenceproxy.go`) getroffen; `modules/inferenceproxy`
besitzt die mandantenspezifische Governance-Konfiguration und die DLP-Policy für
ausgehende Inferenz, die als Eingaben für diese Entscheidung dienen, und
entscheidet selbst nichts über eine Live-Anfrage.
Servergesteuerte Einstellungen erreichen diesen Verkehr nicht: eine
benutzerdefinierte `ANTHROPIC_BASE_URL` umgeht sie vollständig. Der Proxy
schaltet `api.anthropic.com` vor und durchläuft eine kontrollierte Pipeline
**in-band** — Residenz, [Modellzugriff](/de/reference/modules/x-models/),
DLP und die Content-Gates, dann Kontextfenstergrößenbestimmung und Budget — bevor
irgendein Byte weitergeleitet wird. Die
Aufzeichnung ist **standardmäßig pre-forward**: Die autorisierte Intention wird
**vor** der Weiterleitung ins manipulationserkennbare Ledger geschrieben, und ohne
Evidenz keine Weiterleitung (deny-closed). Ein Mandant kann das bewusst
abschalten (`record_mandatory: false`); dann verankert der Proxy die Evidenz
post-forward, best-effort und laut — eine fehlgeschlagene Verankerung wird
gemeldet, niemals verborgen.

Dieser Standard war früher umgekehrt, und der Unterschied ist nicht akademisch:
Ein Mandant, der die Konfigurationsseite nie geöffnet hat, ist genau der, über
den niemand nachgedacht hat — ihn best-effort zu verankern machte die
Evidenzgarantie zum Opt-in für alle, die sich für nichts entschieden hatten.
Zwei Grenzen sollte man lesen statt entdecken. Erstens gilt die Haltung **nur**
für den Moment vor der Weiterleitung: danach hat der Aufruf stattgefunden und
keine Haltung macht ihn rückgängig, dieser Pfad ist also konstruktionsbedingt
eine laute Lücke. Zweitens hat ein Betreiber, der den Audit-Spool auf `degrade`
gesetzt hat, bereits gesagt, was bei Erschöpfung geschehen soll: Für einen
Mandanten, der nie eine Evidenzhaltung gewählt hat, gewinnt dieses erklärte
`degrade`, und der Aufruf wird mit einer protokollierten Lücke weitergeleitet.
Ein Mandant, der ausdrücklich `record_mandatory: true` gesetzt hat, wird
stattdessen abgewiesen — seine eigene Wahl steht über der des Spools.
Der `count_tokens`-Pre-flight zur Größenbestimmung ist selbst Provider-Egress und
läuft daher erst, **nachdem** jedes lokale Content-Gate passiert wurde: Ein durch
DLP oder Firewall verweigerter Prompt wird niemals übertragen, nicht einmal zum
Zählen. Der Proxy ist einer der **vier deny-closed PEPs**, die die Plattform ausliefert.

## Reifegrad, klar benannt

**TEILWEISE.** Die Aufteilung ist ehrlich und bewusst:

- **LIVE** — die mandantenspezifische Governance-Konfiguration und die
  DLP-Policy für ausgehende Inferenz: Autorierung, Persistenz und Audit. Zwei
  Stores unter `/v1/m/inferenceproxy/`: ein Singleton `config` (Toggles je Gate,
  die Fail-Haltung bei Proxy-Ausfall, der Response-DLP-Modus, das
  Aufzeichnungsmandat) und ein `dlp/rules`-Set (eine Regel je
  Sensitivitätsklasse → `allow`|`deny`).
- **OPT-IN, standardmäßig ungemountet** — der eigentliche `/v1/messages`-Listener.
  Er bindet standardmäßig an **Loopback** (`127.0.0.1:8448`), ein Betreiber kann
  jedoch ausdrücklich eine andere Bind-Adresse konfigurieren; er ist
  standardmäßig **fail-CLOSED** (ein Proxy, der nicht entscheiden kann, darf
  nicht weiterleiten) und wird nur gemountet, wenn ein Betreiber ihn
  bereitstellt.

Dieses Modul **entscheidet nichts** über eine Live-Anfrage. Es ist die
dauerhafte, über die Konsole autorierbare Policy, die der Composition Root via
`Policy()` liest; die Entscheidung wird am Rand aus bestehenden Seams
zusammengesetzt (`EvaluateModelAccess`, `CheckBudget`, Residenz,
`ClassifySensitivity`, der Kontextfenster-Check).

## Die In-band-Pipeline

Jedes Gate ist standardmäßig **aktiviert** und bleibt unter seinem eigenen
nativen Opt-in inert, bis es konfiguriert wird — DLP bis zur ersten Regel,
Modellzugriff bis zum ersten Grant, Residenz nur, wenn eine Region gepinnt ist,
Budget, bis ein durchsetzendes Budget existiert. Ein Mandant lockert ein
bestimmtes Gate ausdrücklich, und das Audit hält fest, wer den Perimeter
geöffnet hat. Das Schreiben der DLP-Egress-Policy ist **Admin-Tier**: zu
autorisieren, welche Inhalte das System verlassen dürfen, ist eine privilegierte
Governance-Änderung.

## Bounded Context

- **Minimale Daten by construction.** Keine Zeile, die es persistiert — Config,
  DLP-Regel, Audit — trägt jemals einen Prompt, eine Response, ein Secret oder
  einen erkannten PII-Wert. Bytes, die der Proxy im Flug inspiziert, werden
  fingerprinted (SHA-256) und vom Composition Root im Ledger verankert, niemals
  hier gespeichert.
- Es ist das **dritte Bein** des Proxys: die Protokoll-Shell (parsen,
  weiterleiten, die Bodies abzweigen) ist der identitätsblinde Apache-Connector;
  der kontrollierte Entscheider ist die Engine. Dieses Modul besitzt nur die
  Policy, die beide konsultieren — und hält die Entscheidung damit außerhalb der
  Open-Core-Connector-Grenze.

## Verwandt

- [Modul X — Modell- & Provider-Verwaltung](/de/reference/modules/x-models/) — die
  Modellzugriffs- und oberflächenspezifische Kontextfenster-Policy, die dieser
  Proxy durchsetzt.
- [Claude Code mit Olivares betreiben](/de/how-to/run-claude-code-with-olivares/) —
  der kontrollierte Claude-Code-Pfad, den der In-Process-Hook abdeckt; dieser
  Proxy ist der Fallback-PEP für Aufrufer, die dieser Pfad nicht erreichen kann.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was über die Plattform
  hinweg live, opt-in oder im Entwurfsstadium ist.
