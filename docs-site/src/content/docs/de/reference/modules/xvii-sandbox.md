---
title: "Modul XVII — Agenten-Simulations- & Test-Sandbox"
description: >-
  Isolierte, ephemere Ausführung von Agenten-Szenarien gegen gemockte Tools und
  Ressourcen, deterministische Wiedergabe einer historischen Session und
  Pre-/Post-Deploy-Vergleich zweier Varianten — mit einer ehrlichen, attestierten
  Isolationsgarantie.
---

Modul XVII ist die **Test-Sandbox**: Sie führt ein Agenten-Szenario in einer
isolierten, ephemeren Umgebung aus, gibt eine historische Session deterministisch
wieder und vergleicht zwei Varianten vor einem Deployment. Es ist das Geschwistermodul
von Modul XII (Evals) — XVII **führt in Isolation aus und produziert Outputs**, XII
**misst deren Qualität** — und die beiden sind entkoppelt: keines importiert das andere.
Diese Seite ist die Referenz dafür, was die Sandbox heute tut, und ihre ehrlichen
Grenzen.

## Was es ist

Die Sandbox katalogisiert vom Betreiber verfasste **Szenarien**: eine Abfolge von
Schritt-Inputs plus die gemockten Antworten der Tools und Ressourcen, die ein Lauf
berühren darf. Ein Szenario ist eine synthetische Fixture — keine Secrets, keine
Produktions-Handles — vor dem Persistieren geclampt. Drei Abläufe laufen darauf:

- **Szenario-Simulation** — die Schritte eines Szenarios gegen seine Mocks ausführen
  und Outputs pro Schritt erzeugen (optional gegen eine Evals-Suite bewertet).
- **Replay** — die Input-Zeitleiste einer historischen Session rekonstruieren und sie
  deterministisch gegen Mocks erneut ausführen, sodass derselbe Input denselben Output
  ergibt.
- **Pre-/Post-Deploy-Vergleich** — das *gleiche* Szenario gegen eine Baseline und eine
  Kandidaten-Variante ausführen, beide bewerten und ein Verdikt (`improved` /
  `regressed` / `unchanged` / `inconclusive`) mit dem Delta festhalten.

## Entitäten und die Isolationsgarantie

Das Modul besitzt vier Entitäten: ein veränderliches **scenario**, einen
veränderlichen **run** (`running` → terminal), einen append-only **output** pro Schritt
und einen append-only Pre-/Post-Deploy-**comparison**. Jeder Lauf hält fest, *welcher*
Runner ihn ausgeführt hat, ob dieser Runner `isolated` war, ob der ephemere Zustand
`destroyed` wurde, die Zählungen pro Schritt und — falls ein Scorer verdrahtet war —
die Suite, den Score und das Pass-Verdikt.

Isolation ist eine Eigenschaft der Leitung, pro Lauf attestiert, keine Behauptung. Der
standardmäßige In-Process-Runner ist **isoliert per Konstruktion**: Er erhält nur die
Step-and-Mock-Spezifikation und hält kein Handle zum Store, zum Netzwerk oder zu
irgendeinem Secret; ein Schritt, der eine in den Mocks fehlende Ressource anfragt,
ergibt einen deterministischen Mock-Miss-Marker und erreicht nie eine reale Ressource;
der Zustand lebt im Aufruf und wird beim Rücksprung verworfen, sodass der Lauf
`destroyed` festhält. Bei Bereitstellung durch den Betreiber steht eine
**OS-Level-Runtime** hinter derselben Schnittstelle — eine ephemere, gehärtete,
egress-kontrollierte Instanz, deren Backend (gVisor oder Firecracker-microVM) *per
Policy* gewählt und durch Preflight gegated wird. Jeder Lauf hält das reale Backend und
sein `isolated`-Flag fest, sodass ein degradiertes oder portables Backend sichtbar und
auditierbar ist, nie versteckt.

## Was es konsumiert und produziert

Die Sandbox emittiert nicht auf dem Event-Bus; sie produziert **persistierte Beweise**,
die andere Module lesen, ohne an sie gekoppelt zu sein. Ihre Outputs werden von Modul
XII über einen nur im Composition-Root verdrahteten Adapter bewertet — die beiden
Geschwister teilen sich einen dünnen Port-Vertrag, keinen Import. Ihr
Pre-/Post-Deploy-Vergleich ist der **Entscheidungsbeweis**, den das Deployment-Modul
liest, um eine Promotion zu gaten, und er speist die Regressions-Baseline, die XII
verfolgt. Das Starten eines Laufs, eines Replays oder eines Vergleichs ist eine
**privilegierte, mandantengebundene, auditierte** Aktion (Editor und höher zum
Ausführen; der Deploy-Vergleich ist eine Admin-Entscheidung).

:::caution[Ehrliche Grenzen]
- **Die Standard-Runtime ist nur synthetisch.** Ohne eine vom Betreiber bereitgestellte
  OS-Level-Runtime ist der In-Process-Mock-Runner das Backend: Er ist isoliert per
  Konstruktion, führt aber nur gegen Mocks aus, kann also kein reales Ziel erreichen und
  keine adversariale Sonde gegen Live-Infrastruktur stützen (Modul XVIII behält seinen
  eigenen sicheren Standard, bis die Runtime bereitgestellt ist). Dies ist ehrlich,
  nicht degradiert — ein Standard-Deployment ist voll funktionsfähig.
- **Bereitgestellt-aber-unfähig schlägt geschlossen fehl.** Wenn OS-Level-Isolation
  angefordert wird und dem Host das Primitiv fehlt, verdrahtet die Engine dasselbe und
  **jeder Lauf schlägt geschlossen fehl** — er stuft nie still auf den synthetischen
  Runner herunter und täuscht nie eine microVM vor. Ein Lauf auf einem Host ohne
  Isolation wird als nicht isoliert festgehalten, nie als geschützt.
- **Kein Scorer verdrahtet ⇒ „ausgeführt, nicht bewertet."** Ein Lauf, der eine
  Suite-Referenz ohne Scorer-Adapter trägt, wird als ausgeführt, aber unbewertet
  festgehalten — nie als stiller Pass.
- **Replay ist ehrlich über Lücken.** Wenn die History-Quelle keine geordnete
  Zeitleiste rekonstruieren kann, wird der Replay als degradiert mit null Schritten
  gemeldet, nie fabriziert.
- **Keine synthetische Datengenerierung.** Dies ist nur ein dokumentierter
  Post-v1-Erweiterungspunkt; das Modul liefert keinen Generator aus, exponiert keine
  Route dafür und produziert null Samples.
:::

## Verwandt

- [Modul XII — Qualität, Evals & Testing](/de/reference/modules/xii-evals/) — das Geschwistermodul, das die Outputs bewertet.
- [Modulkatalog](/de/reference/modules/overview/) — wo XVII sitzt und die Govern/Actuate-Aufteilung.
- [Architektur-Überblick](/de/explanation/architecture/overview/) — die Intelligence-Schicht.
- [Govern and approve](/de/how-to/govern-and-approve/) — auf ein Pre-/Post-Deploy-Verdikt reagieren.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die deny-closed-Nähte im gesamten Produkt.
