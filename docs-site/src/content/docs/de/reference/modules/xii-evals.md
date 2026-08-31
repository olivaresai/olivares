---
title: "Modul XII — Qualität, Evals & Testing"
description: >-
  Qualitätsmessung: Bewertung von Kandidaten-Outputs gegen versionierte Golden-Suites
  mit austauschbaren Scorern (einschließlich eines fail-closed LLM-Judge) und
  Umwandlung des Ergebnisses in den kanonischen, modulübergreifenden Nachweis, den
  andere Module konsumieren.
---

Modul XII beantwortet eine einzige Frage — *macht mein Agent immer noch das Richtige?* —,
indem es Kandidaten-Outputs **gegen versionierte Golden-Suites bewertet (scoring)** und das
Ergebnis als kanonischen, modulübergreifenden Nachweis ausgibt. Es ist ein Modul der
Intelligence-Schicht: es **misst**, es führt das untersuchte Subjekt nicht aus und es
agiert nicht auf der Infrastruktur. Diese Seite ist die Referenz dafür, was das Evals-Modul
heute leistet, und für seine ehrlichen Grenzen.

## Was es misst (und was es niemals ausführt)

XII ist eine Mess-, keine Ausführungsschicht. Ein Kandidaten-Output erreicht es bereits
fertig erzeugt — aus der Testing-Sandbox (Modul XVII), aus CI, inline in der Anfrage oder
als gesampletes Signal aus einer realen Session — und XII bewertet ihn gegen die Fälle einer
Suite. **Das einzige Modell, das XII jemals aufruft, ist der Judge** (für den `llm_judge`-Scorer);
es führt niemals den untersuchten Agenten oder das Modell selbst aus. Outputs zu erzeugen ist
Aufgabe der Sandbox, nicht von XII.

Das Scorer-Set ist **austauschbar**. Deterministische, reine Built-ins decken die gängigen
Verträge ab — `exact`, `contains`, `not_contains`, `regex`, `json_valid`, `json_equal`
und `numeric_range`. Daneben steht ein **`llm_judge`**-Scorer, der über den Judge-Port ein
Modell aufruft, um anhand einer Rubrik zu bewerten.

## Suites, Runs und das kanonische Artefakt

Eine **Suite** ist ein versioniertes Golden-Dataset: sie enthält ihre Fälle, einen Default-Scorer,
einen Bestehensschwellenwert und einen Regressionsschwellenwert. Fälle sind **append-only und
unveränderlich pro Version** — die Korrektur eines Falls prägt eine neue `suite_version`, niemals
eine Bearbeitung an Ort und Stelle, sodass das Dataset, das ein vergangenes Urteil erzeugt hat,
stets rekonstruierbar ist.

Ein **Run** bewertet jeden Fall einer Suite, aggregiert einen `score` und eine `pass_rate` und
persistiert drei Dinge: append-only Nachweise pro Fall, ein veränderbares Run-Aggregat und ein
zentrales **`EvalResult`** — das kanonische Artefakt (`Suite`, `SubjectKind`, `SubjectID`,
`Score`, `Passed`, `OccurredAt`, `Metrics`), das Compliance (XIII) und die UI lesen, **ohne die
eigenen Tabellen von XII zu kennen**. Runs werden synchron ausgeführt; der SSE-Stream eines
Runs *gibt den persistierten Run wieder* (Frames pro Fall, dann eine Zusammenfassung), er löst
nichts aus. Eine Regression gegenüber einer Baseline setzt `regressed` und schreibt ein zentrales
**`Finding`** (`Kind = eval_regression`), das nach bestem Bemühen auf dem Bus als
[`finding.reported`](/de/reference/events/) ausgegeben wird, damit Delivery-Module (Health/Benachrichtigungen)
es routen. Auf der Leseseite aggregieren **Scorecards** Pass-Rate, mittleren Score und Trend pro
Subjekt und exportieren als CSV/JSON.

## Minimal-data, konstruktionsbedingt

Der Kandidaten-Output wird **niemals persistiert** — aus keiner Quelle. Ein Ergebnis pro Fall
speichert nur einen Einweg-Detail-Hash und ein geklammertes, bereinigtes Label für die UI; die
Maskierung erledigt der Handler vor der Speicherung, sie wird niemals vom Store vorausgesetzt. Der
**Monitor** bewertet *Verhaltenssignale* einer realen Session — ihren Zustand, die Finding-Anzahl,
die maximale Schwere und Token-/Kostenwerte (aus den zentralen Signalen `Session`, `Finding` und
`CostRecord`) — und **niemals den rohen Output-Text**, den die Plattform überhaupt nicht persistiert.
Golden-Fixtures sind die eine begrenzte Ausnahme: betreiberautorisiert, opt-in, nicht-produktive
Inhalte, vom Handler vor dem Schreiben geklammert, damit eine Suite tatsächlich ausgeführt werden kann.

## Judge-Kalibrierung, Bias-Minderung und das CI-Regressions-Gate

Den Urteilen des Judge wird **erst vertraut, nachdem sie gemessen wurden**. Ein menschlich
gelabeltes Kalibrierungs-Set (erstellt mit der geführten Session `olivares evals label`) speist
einen **Kalibrierungs-Run**, der den Judge gegen die menschliche Referenz misst: prozentuale
Übereinstimmung mit ihrem 95%-Wilson-Intervall, **Cohens Kappa** (Übereinstimmung allein ist
unter Klassenungleichgewicht nicht vertretbar), Sensitivität/Spezifität mit ihren Nennern und
eine Verbosity-Bias-Korrelation. Der Report ist append-only Nachweis; das Ziel —
Übereinstimmung ≥ 0,85 **und** ein definiertes Kappa ≥ 0,6 — kann pro Run angehoben, aber niemals
gesenkt werden. Ein Set, dessen menschliche Labels alle „pass" lauten, kann keine zufallskorrigierte
Übereinstimmung messen und zertifiziert nichts.

Bias-Minderung ist eingebaut und *gemessen*: der Judge-Prompt erzwingt die Begründung **vor**
dem Urteil (die Analyse wird im Flug verworfen — Minimal-data) und weist an, Länge nicht zu
belohnen; der opt-in paarweise Modus des A/B-Vergleichs bewertet jeden geteilten Fall zweimal mit
vertauschter Präsentationsreihenfolge, erklärt einen Sieger **nur, wenn beide Reihenfolgen
übereinstimmen**, und berichtet die gemessene `position_consistency`-Rate.

Das **Regressions-Gate** (`POST /gate`, CLI `evals gate`) verwandelt all dies in ein
blockierendes CI-Urteil: eine Regression gegenüber der Baseline, eine Pass-Rate unter dem
Suite-Schwellenwert oder ein **unkalibrierter Judge** lässt das Gate scheitern (Exit 1); ein
fehlender Judge-Credential degradiert zu einer *deklarierten* Warnung, niemals zu einem stillen
Pass. Die Judge-Kosten in CI werden gesteuert durch ein deterministisch geseedetes Fall-Sample,
einen Urteils-Cache mit Schlüssel aus Inhalt + Judge-Modell-Pin + Prompt-Version sowie eine
FinOps-Budget-Vorprüfung, die sich weigert, über eine Obergrenze hinaus auszugeben. Der einzige
Ausweg aus einem gescheiterten Gate ist das **gesteuerte Override** — Admin-Stufe, schriftliche
Begründung, auditiert —, das das *effektive* Urteil ändert, das CI erneut prüft, niemals das
aufgezeichnete. Jede berichtete Rate kommt mit ihrem Nenner und 95%-Intervall; siehe
`docs/EVAL-METHODOLOGY.md` im Repository für die vollständige Methodik und Quellen.

:::caution[Ehrliche Grenzen]
- **`llm_judge` ist fail-closed, niemals ein falsches Pass.** Der Modellaufruf ist eine deklarierte
  Naht: ohne verdrahteten Judge gibt der `llm_judge`-Scorer `skipped` zurück (aus dem Nenner
  ausgeschlossen), niemals ein stilles Pass. Der Composition-Root injiziert den echten
  Judge-Adapter; bis dahin werden bewertete Fälle ehrlich als nicht evaluiert berichtet.
- **Das Gate blockiert Merges, nicht Infrastruktur.** Das Regressions-Gate gibt ein Urteil zurück,
  das eine CI-Pipeline auf ihren Exit-Code abbildet; XII deployt weiterhin nichts und löst nichts
  aus. Ein unkalibrierter Judge kann sein eigenes Gate nicht bestehen — die Kalibrierung wird
  gegen menschliche Labels gemessen, niemals vorausgesetzt.
- **XII führt das Subjekt nicht aus.** Es bewertet Outputs, die ihm übergeben werden; es führt
  niemals den getesteten Agenten oder das getestete Modell aus. Der einzige Modellaufruf, den es
  macht, ist der Judge.
- **Monitoring sind Signale, kein Text.** Das Monitoring realer Sessions bewertet Minimal-data-
  Ergebnissignale — niemals rohen Output, der niemals persistiert wird. Das Fehlen eines
  überwachten Signals ist kein Beweis für ein Verhalten.
- **Keine Aktuierungsfläche.** XII steuert und beobachtet Qualität; es deployt nichts, löst nichts
  aus und gated keine Infrastruktur. Das Pre-/Post-Deploy-*Urteil*, das es liefert, ist ein
  Nachweis, auf den das Deploy-Modul hin agieren kann — siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo XII sitzt und die Trennung von Govern/Actuate.
- [Event-Bus-Referenz](/de/reference/events/) — das `finding.reported`-Event, das eine Regression ausgibt.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Intelligence-Schicht.
- [Steuern und genehmigen](/de/how-to/govern-and-approve/) — auf ein Regressions-Finding reagieren.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die deny-closed Nähte über das Produkt hinweg.
