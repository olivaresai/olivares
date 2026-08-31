> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0012: Verteilte Erfassung — Collectoren pushen über gRPC + mTLS an den Kern

- **Status:** accepted
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares (boot decision CB-1)
- **References:** roadmap boot decisions (CB-1 → option C); runtime-ingestion contract

## Kontext und Problemstellung

Die Erfassungsebene brauchte eine Topologie-Entscheidung. Collectoren beobachten auf den
Hosts des Kunden; der Kern aggregiert. Die Optionen reichten von rein in-process bis zu
einem vollständig verteilten Push-Modell, mit Auswirkungen auf Isolation, die
Netzwerk-Vertrauensgrenze und das Packaging.

## Entscheidungstreiber

- Die Datenebene auf der Infrastruktur des Kunden halten, mit einem gehärteten
  Netzwerkübergang.
- Das Single-Binary für den einfachen Fall bewahren.
- Collector-Abhängigkeiten vom Kern isolieren.

## Betrachtete Optionen

- **C — Remote Push:** ein Collector führt die Source-Connectoren lokal aus und **pusht**
  Beobachtungen über **gRPC + mTLS** an den Kern, **ohne eingehenden Listener** auf dem
  Collector.
- **B — Out-of-Process lokal:** Connectoren als lokale Subprozesse (AutoMTLS), das
  Single-Node-Substrat.
- **A — In-Process:** Quellen in das Binary gelinkt (First-Party-Fast-Path).

## Entscheidung

Gewählte Option: **C (Remote Push) als verteiltes Ziel**, mit B als
Single-Node-Substrat und A beibehalten als In-Process-Fast-Path für First-Party-Quellen.
Alle Transporte gehen in v1 ein; C wird **nicht** zurückgestellt. Der Mechanismus lebt in
der Runtime; das verteilte Packaging (DaemonSet/Helm) wird mit der Supply-Chain-Arbeit
ausgeliefert.

### Konsequenzen

- **Gut:** Die Daten überqueren die Netzwerkgrenze gehärtet (mTLS + Bearer + authz); der
  Collector exponiert keinen eingehenden Port; das Single-Binary bleibt erhalten.
- **Schlecht / Kompromisse:** mehr bewegliche Teile für das verteilte Deployment.
- **Neutral:** Der Single-Binary-Standard nutzt die In-Process-/Lokal-Subprozess-Pfade.

## Warum die Alternativen verworfen wurden

- Weder **A** noch **B** allein deckt die Multi-Host-Skalierung ab; sie bleiben als
  Fast-Path bzw. als Single-Node-Substrat erhalten, nicht als die verteilte Antwort.
