---
title: "Eigenmodell-Fine-Tuning & lokale Inferenz — Ausführung (geplant)"
description: >-
  Was auf der Eigenmodell-Seite geplant bleibt: dass die Plattform Fine-Tuning-Jobs
  selbst ausführt und lokale Inferenz selbst bedient. Die Eigenmodell-Registry, die
  Admission signierter Modelle, die Lineage-Datensätze und die AIBOM-Belege werden
  bereits als Modellbetrieb ausgeliefert; diese Seite ist ehrlich über die ausführende
  Hälfte, die es nicht wird.
---

Die Eigenmodell-Geschichte — die Governance von **Modellen, die das Unternehmen selbst
trainiert oder hostet** — teilt sich in zwei Hälften, und nur eine davon ist noch geplant.

Die **governende Hälfte wird heute ausgeliefert** als
[Modul XXIII — Modellbetrieb](/de/reference/modules/xxiii-model-operations/): eine
versionierte **Registry eigener Modelle** (`hosted`, `fine_tuned`, `imported`), das
**Admission**-Gate für signierte Modelle, **Lineage-Datensätze für Datasets und
Fine-Tuning-Jobs**, governte **Deployment-Datensätze für lokale Inferenz** (vLLM, Ollama,
llama.cpp, andere) mit Enforce-Signed-Nachprüfung zum Deploy-Zeitpunkt, und
**AIBOM-/Model-Card**-Erzeugung mit ledger-verankertem Sealing. Seine Entitäten und
Endpoints sind deklariert und werden über die Beta-Modul-Routen bedient
(`/v1/m/models/owned-models`, `/v1/m/models/model-versions`,
`/v1/m/models/finetune-jobs`, `/v1/m/models/inference-deployments`,
`/v1/m/models/aiboms`, …) — siehe die [Modul-Routen-Referenz](/reference/api-beta/).

Diese Seite behandelt die **ausführende Hälfte, die geplant und bewusst nicht gebaut
ist**: dass die Plattform diese Arbeit selbst *ausführt*.

## Was heute ausgeliefert wird (an anderer Stelle)

Die Governance eigener Modelle ist real und auf der Seite
[Modellbetrieb](/de/reference/modules/xxiii-model-operations/) dokumentiert:

- eine **Registry eigener Modelle** mit unveränderlichen Versionen, sodass ein
  fine-getuntes oder selbst gehostetes Modell eine erstklassige, governte Entität ist und
  kein unverwalteter Endpoint;
- **Fine-Tuning-Jobs als Lineage-Datensätze** — Inventar extern ausgeführter
  Trainingsarbeit und der Modellversion, die jeder Job erzeugt hat;
- **lokale Inferenz-Deployments als governte Datensätze** — die Serving-Runtimes, die Sie
  betreiben, unter Admission-Durchsetzung (`require_signed`) und Audit.

## Was geplant bleibt

- **Fine-Tuning-Jobs ausführen.** Das ausgelieferte Modul erfasst Status und Lineage von
  andernorts ausgeführter Fine-Tuning-Arbeit; die Plattform startet, bricht ab oder führt
  niemals einen Trainingsjob aus und speichert weder Gewichte noch Dataset-Inhalte. Eine
  Pipeline, die Fine-Tuning von der Plattform aus *ausführt*, ist geplante Arbeit.
- **Lokale Inferenz bedienen.** Deployments sind governte Datensätze von Runtimes, die
  der Operator betreibt; die Plattform hostet oder bedient Inferenz nicht selbst.
  First-Party-Serving lokaler Inferenz ist geplante Arbeit.

Für diese ausführende Hälfte ist kein Job-Schema, kein Scheduler-Vertrag und kein
Serving-Runtime-Vertrag deklariert, und diese Seite erfindet bewusst keinen.

## Warum geplant und nicht ausgeliefert

Die Plattform ist so gebaut, dass sich jede Fähigkeit anbinden lässt, ohne den Rest neu
zu architektieren — die Ausführung kann später auf den ausgelieferten
Governance-Oberflächen aufsetzen. Sie wurde durch eine explizite Produktentscheidung
**nach** v1 platziert: Die Priorität des ersten Releases ist die Governance der Modelle
und Agenten, die eine Organisation bereits betreibt, und das Ausführen von
Training/Serving ändert diesen Kernwert nicht genug, um um v1-Aufwand zu konkurrieren.

Wenn sie gebaut wird, ist ihre natürliche Naht bereits ausgeliefert: Ein ausgeführtes
Fine-Tuning würde eine Modell-**Version** in der Registry des
[Modellbetriebs](/de/reference/modules/xxiii-model-operations/) erzeugen und dasselbe
**Admission**-Gate für signierte Modelle passieren wie jedes extern erzeugte Artefakt,
während die Richtlinie für den Anbieter-Stack in der
[Modell- & Anbieterverwaltung](/de/reference/modules/x-models/) verbleibt.

:::caution[Ehrliche Grenzen]
- **Die governenden Oberflächen sind ausgeliefert, die ausführenden nicht.** Lesen Sie
  diese Seite nicht als Verneinung von Registry, Admission, Lineage-Datensätzen,
  Deployment-Governance oder AIBOM-Belegen — sie existieren und sind im
  [Modellbetrieb](/de/reference/modules/xxiii-model-operations/) dokumentiert.
- **Heute existiert keine Ausführungsoberfläche.** Es gibt keine Trainings-Pipeline,
  keinen Fine-Tune-Job-Scheduler und kein First-Party-Inferenz-Serving im ausgelieferten
  Binary, und keine Entität, kein Endpoint und kein Event ist dafür deklariert — nicht
  einmal eine ablehnende Schnittstelle.
- **Nichts hier ist ein Versprechen eines Datums oder einer Tiefe.** Der Umfang oben ist
  die geplante Richtung; Job-Schema und Runtime-Verträge werden entworfen, wenn gebaut
  wird. Sie bleiben bewusst unspezifiziert, statt fabriziert zu werden.
:::

## Verwandt

- [Modul XXIII — Modellbetrieb](/de/reference/modules/xxiii-model-operations/) — die ausgelieferte Governance-Oberfläche für eigene Modelle: Registry, Admission, Lineage, Deployments, AIBOM.
- [Modulkatalog](/de/reference/modules/overview/) — die 30 ausgelieferten Module und wo die Eigenmodell-Arbeit sitzt.
- [Modul X — Modell- & Anbieterverwaltung](/de/reference/modules/x-models/) — der ausgelieferte Nachbar, der den Anbieter-Modell-Stack governt.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — der Vertrag „breit beobachten / auf einer Teilmenge handeln" und was „geplant" bedeutet.
