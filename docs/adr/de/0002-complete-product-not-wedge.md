> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0002: Das vollständige Produkt ausliefern (28 Module), keinen Wedge

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** product decisions register (P1); module catalog (the 28 modules)

## Kontext und Problemstellung

Eine verbreitete Go-to-Market-Strategie für ein Infrastrukturprodukt ist ein schmaler
„Wedge“ — eine scharf umrissene Fähigkeit ausliefern, einen Brückenkopf gewinnen und
später expandieren. Für Olivares AI war der naheliegende Wedge die Read/Write Access Map
allein. Die Frage war, ob der Wedge oder die vollständige Control Plane veröffentlicht
werden sollte.

## Entscheidungsfaktoren

- Erster Eindruck: Enterprise-Käufer (CTO/SOC/Security) bewerten eine Control Plane als
  Plattform, nicht als Feature.
- Die R/RW Map ist *innerhalb* einer vollständigen Plattform wertvoller als als
  eigenständiges Werkzeug.
- Vermeidung von Re-Architektur: eine modulare Plattform nimmt neue Module ohne Umbau auf.

## Betrachtete Optionen

- **Vollständiges Produkt** — alle 28 Module als eine kohärente Plattform ausliefern
  (eigenes Modell-Management / Fine-Tuning ist eine geplante Fähigkeit, nicht eines der
  28 Module).
- **Schmaler Wedge** — die R/RW Map allein ausliefern, später expandieren.

## Ergebnis der Entscheidung

Gewählte Option: **vollständiges Produkt**. Das erste Release ist die komplette
Plattform, aufgebaut rund um Claude und Claude Code — Inventar, Live-Sessions, die R/RW
Map, Governance, Source-/Credential-Scoping, Deployment, Knowledge, Security,
Aufzeichnung privilegierter Sessions, Modell-/Provider-Management, der Inline-Inference-
Proxy, FinOps, Evals, Compliance, der SIEM-Forwarder, Catalog, Output-Integrationen,
Eventing, Voice, Sandbox, Red-Teaming und Health — mit der eigenen API der Engine,
Mandantenfähigkeit und Dashboards als Core-/Konsolen-Fähigkeiten. Die R/RW Map ist **eine
zentrale differenzierte Fähigkeit innerhalb** dieses Produkts, nicht das Produkt selbst.

### Konsequenzen

- **Gut:** eine vollständige, glaubwürdige Plattform vom ersten Tag an; die Access Map
  landet im Kontext.
- **Schlecht / Trade-offs:** eine deutlich größere v1-Oberfläche, die gebaut und ehrlich
  gehalten werden muss; die Tiefe variiert je Modul und muss ehrlich dokumentiert werden
  (siehe *Ehrlichkeit & Grenzen*).
- **Neutral:** eigenes Modell-Management / Fine-Tuning ist eine geplante Fähigkeit, nicht
  eines der 28 ausgelieferten Module.

## Warum die Alternativen verworfen wurden

- **Schmaler Wedge** — verworfen: er verkauft ein Plattformprodukt unter Wert und birgt
  das Risiko, dass die R/RW Map als ein Commodity-„Session-Viewer“ wahrgenommen wird statt
  als die Least-Privilege-Drift-Engine, die sie ist.
