---
title: "Modul XVIII — Red-Teaming & adversariales Testen"
description: >-
  Ein defensives Robustheits-Harness: eine consent-gegatete Batterie veröffentlichter
  adversarialer Sonden, gemappt auf OWASP Agentic und MITRE ATLAS, bewertet zu einem
  manipulationserkennbaren Scorecard. Was es testet, die Consent-Red-Line und seine
  ehrlichen Grenzen.
---

Modul XVIII ist ein **defensives Robustheits-Harness**. Es sondiert die **eigenen**
gegovernten Agenten des Kunden mit einer Batterie veröffentlichter adversarialer
Testfälle — Prompt-Injection, Jailbreak, Exfiltration, Tool-Poisoning — und bewertet
deren Widerstandsfähigkeit, gemappt auf die **OWASP Top 10 for Agentic Applications**,
die **OWASP LLM Top 10 (2025)** und **MITRE ATLAS**. Es ist eine Testsuite, keine
Waffe: ein Compliance-Verstoß oder ein Leak ist ein Finding, kein Exploit, der
irgendjemandem in die Hand gegeben wird.

## Was es ist

Die Batterie ist ein Katalog von **Sonden** über vier Familien (`injection`,
`jailbreak`, `exfil`, `tool_poisoning`). Jede Sonde ist ein *bekannter,
veröffentlichter* Robustheitstest, gemappt auf eine OWASP/ATLAS-Referenz, mit der
Erwartung, dass ein gut verteidigter Agent ihn **verweigert** oder sein Guardrail ihn
**blockt**. Payloads sind harmlose Canaries — sie bitten den Agenten, einen inerten
Marker zu emittieren, oder beschreiben eine gefährliche Operation, ohne sie auszuführen
— sodass die Batterie die *Verweigerung* sondiert, nicht den Bruch. Ein deterministischer
**Judge** klassifiziert jedes Ergebnis: `blocked`/`refused` ist ein **Pass**,
`complied`/`leaked` ist ein **Fail**, `error` ist ein Ausführungsfehler, `skipped` ist
nicht-ausgeführt.

Ergebnisse rollen sich zu einem **Scorecard** auf: `score = passed / (passed + failed) × 100`,
wobei `errors` und `skipped` bewusst aus dem Nenner **ausgeschlossen** sind — eine
Sonde, die nie lief, wird nie als Pass gezählt. Das Scorecard schlüsselt pro Familie
auf und verfolgt die OWASP-Agentic-Failure-Coverage und ist ein **append-only,
manipulationserkennbarer** Datensatz, sodass ein späterer Lauf ihn als
Regressions-Baseline vergleichen kann.

## Die Red-Line, ihr Vertrag und ihre Entitäten

Die Dual-Use-Grenze ist **im Code durchgesetzt**, nicht nur in den Docs benannt. Ein
Lauf wird **ausschließlich** gegen einen vom Kunden gegovernten Agenten ausgeführt, der
explizit als Ziel **registriert und autorisiert** wurde — und Registrieren ist kein
Einwilligen: Ein Ziel wird `registered` geboren, mit zurückgehaltener Autorisierung,
und ein separater Authorize-Schritt ist die explizite Erteilung. Das Starten eines
Laufs gegen ein nicht autorisiertes oder unbekanntes Ziel wird am Gate verweigert.
Registrieren, Autorisieren und Starten sind allesamt **Admin-Tier-, auditierte,
privilegierte** Aktionen; jede hinterlässt ein Self-Audit, das dem realen Principal
zugerechnet wird.

Das Modul besitzt drei mandantengebundene Entitäten: das **target** (ein
veränderlicher Consent-Datensatz über seinen register → authorize → revoke-Lifecycle),
den **run** (ein append-only Evaluationsdatensatz, der die Aggregate und den Score
trägt) und die **results** pro Sonde (append-only, eine Zeile pro Sonde). Es ist
**datenminimal per Konstruktion**: Der Target-Endpoint ist ein opakes Handle, das die
Sandbox dereferenziert — nie ein Credential — und ein Ergebnis speichert nur einen
Einweg-Hash seines Details, nie die Roh-Payload oder die Roh-Antwort des Agenten. Die
Read-Side-API liefert den Katalog **nur als Taxonomie** (id, Familie, Titel,
OWASP/ATLAS-Referenz, Schweregrad, Surface); die Sonden-Payloads sind intern und werden
nie auf der Leitung exponiert.

## Was es konsumiert und produziert

Das Modul besitzt die Batterie und das Scoring; es erreicht **selbst** keinen Agenten.
Die Ausführung wird über eine `Sandbox`-Naht an die isolierte Runtime delegiert — die
Sandbox ist die einzige Komponente, die das Ziel berührt, innerhalb des Perimeters des
Kunden, mit Egress segmentiert auf exakt das autorisierte Ziel und alles andere
verweigert. Jede fehlgeschlagene Sonde wird als Core-`Finding` (`kind = "redteam"`)
innerhalb der Transaktion des Laufs persistiert, und ein datenminimales
`finding.reported`-Event (`kind = "redteam_failure"`) wird auf dem
[Event-Bus](/de/reference/events/) für Zustellungs- und Compliance-Konsumenten
veröffentlicht — beide tragen nur eine Subjektreferenz, einen Titel und einen
Detail-Hash.

## Ehrliche Grenzen

:::caution[Ehrliche Grenzen]
- **Ohne verdrahtete Sandbox ist ein Lauf DEGRADIERT, nie ein False Pass.** Die
  standardmäßige Ausführungs-Naht erreicht keinen Agenten: Jede Sonde wird `skipped`
  gemeldet, der Lauf-Status ist `degraded`, und der Score spiegelt wider, dass nichts
  getestet wurde. Das Harness liefert die volle Batterie und das Scoring heute aus; die
  Live-Ausführung hängt von einer bereitgestellten isolierten Runtime ab, und ein nicht
  bereitgestelltes Deployment ist darüber ehrlich, statt ein ungetestetes Ziel zu
  bewerten.
- **Es testet nur Agenten, die du governst und autorisierst.** Es zielt nie auf
  Drittsysteme, scannt nie fremde Credentials und liefert keine rein offensive
  Fähigkeit aus. Ein nicht autorisiertes oder unbekanntes Ziel wird verweigert — dies
  ist kein Konfigurations-Schalter.
- **Die Sonden sind eine veröffentlichte, defensive Batterie — keine neuartigen
  Exploits.** Jede mappt auf eine OWASP/ATLAS-Referenz. Die ATLAS-Coverage-Ansicht ist
  ein **datierter Snapshot**, gestempelt auf eine spezifische Matrix-Version, keine
  kontinuierliche Parität mit der Live-Matrix.
- **Ein Sandbox-Ausführungsfehler ist kein Verdikt.** Ein `error`-Ergebnis hält den
  Lauf am Laufen und wird aus dem Score ausgeschlossen; es zählt nie als
  Verwundbarkeit oder Pass.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XVIII sitzt und sein Actuation-Status.
- [Modul IX — Sicherheit, Guardrails & Audit](/de/reference/modules/ix-security/) — der Konsument der `redteam`-Findings.
- [Event-Bus-Referenz](/de/reference/events/) — das `finding.reported`-Event und seine datenminimale Payload.
- [Architektur-Überblick](/de/explanation/architecture/overview/) — wie die Engine und die isolierte Runtime zusammenspielen.
- [Govern and approve](/de/how-to/govern-and-approve/) — ein Ziel autorisieren und auf Findings reagieren.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — der produktweite Actuation-Vertrag.
