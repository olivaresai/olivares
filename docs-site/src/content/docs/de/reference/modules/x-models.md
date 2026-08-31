---
title: "Modul X — Modell- & Provider-Management"
description: >-
  Die Governance-Schicht über dem gesamten KI-Modell-Stack — Claude, OpenAI, Gemini
  und lokale Inferenz. Ein versionierter Referenzkatalog, eine Capability-Matrix und
  eine Routing-Policy, die eine Primär- + Fallback-Kette auflöst; es routet, führt
  den Modellaufruf aber noch nicht aus.
---

Modul X steuert den **gesamten KI-Modell- und Provider-Stack** — Claude, OpenAI, Gemini und
lokale Inferenz, nicht nur einen Anbieter. Es ist ein Modul der **Core-Schicht**, das
*oberhalb* der Modell-/Provider-Konnektoren sitzt: Es implementiert weder eine
Provider-Integration noch das Inferenz-Gateway neu. Was es besitzt, ist die
**Governance-Schicht** — ein versionierter Katalog, eine vendor-übergreifende
Capability-Matrix und benannte Routing-Policy.

## Was es ist

Das Modul verwandelt die nackten `Provider`/`Model`-Entitäten, die das Inventar (Modul I)
entdeckt, in einen gesteuerten Katalog. Zwei Hälften:

- **Ein deklarierter Referenzkatalog** — eine im Repo versionierte, vom Operator
  überschreibbare Tabelle von Modellfamilien mit ihren deklarierten API-Feature-Capabilities
  und **Listenpreis-Defaults**. Preise sind mit dem Datum gestempelt, an dem sie deklariert
  wurden (`pricing_as_of`), sind explizit *Defaults, die gegen die Preisseite jedes Providers
  zu verifizieren sind*, und sind niemals erfundene Telemetrie. Eine Familie ohne passenden
  Eintrag bleibt **ohne Preis**, statt einen erfundenen Preis zu bekommen.
- **Anreicherung des Live-Estate** — das Modul lauscht auf den
  [`cost.sampled`](/de/reference/events/)-Stream und reichert die entdeckten
  `Model`/`Provider`-Entitäten mit Familie, Context Window, Modalität, Pro-Token-Preisgestaltung
  und dem Capability-Set an (die Preisfelder überlässt das Inventar ihm).

Das Capability-Vokabular ist eine **vendor-übergreifende Matrix** — der vollständige
Claude-Stack (Prompt-Caching, Batch, Files, Citations, Extended Thinking, Computer Use, das
Memory Tool, Context Management, Vision/PDF, Structured Outputs) plus die Analoga, die jeder
andere Anbieter tatsächlich offenlegt — sodass die UI eine Matrix rendert und eine
Routing-Policy eine Capability *über* Anbieter hinweg verlangen kann. Die Claude-Familien
sind nach Familie katalogisiert (`claude-opus`, `claude-sonnet`, `claude-haiku`, `claude-fable`, `claude-mythos`), wobei
deprecated/legacy Versionen unter längeren Präfixen geführt werden, damit aktuelle IDs auf
die aktuelle Preisstufe auflösen.

## Sein Vertrag & seine Entitäten

Routing ist die Aktuierungsoberfläche, und sie ist **routing-only**:

- **Routing-Policy** wird auf der Kern-`Policy`-Entität persistiert (`Kind="routing"`):
  benannte Selection-/Fallback-/Version-Pinning-Policies (cheapest-first, lowest-latency,
  capability-ordered oder ein gepinntes Modell). `POST …/routing-policies/{id}/resolve` löst
  eine Policy gegen das gesteuerte Estate auf und liefert eine **Primär- + Fallback-Kette**
  mit der Begründung für die Wahl zurück. Dies ist **read-only**: Es berechnet eine Auswahl,
  die der Konnektor/das Gateway dann ausführt — das Modul führt **keine Inferenz** durch.
- **API-Key-/Workspace-Governance** ist **ausschließlich Minimal-Data-Metadaten** — welcher
  Agent oder welches Team welches Credential nutzt, getragen als maskierter Hinweis, niemals
  der Secret-Wert.
- Ein read-only **Anthropic Rate-Limit-Inventar** (die Obergrenzen, die ein Gateway oder
  Proxy synchron halten muss) wird als konsultierbares Inventar bereitgestellt; es ist
  niemals ein Steuerelement, das das Modul mutiert, und es degradiert zu einer ehrlichen
  *unavailable-with-reason*-Antwort, wenn der read-only Admin-Konnektor nicht bereitgestellt
  ist.

Katalog- und Feature-Lesezugriffe sind nicht sensibel und werden auf der Viewer-Stufe
gegated; Routing- und Key-Governance-Mutationen sind eine auditierte Änderung auf
Editor-Stufe; der Pfad der gesteuerten Ausführung ist eine Aktion auf Admin-Stufe, die sich
vom Resolve auf Lesestufe unterscheidet. Die Routen werden in der separaten **Beta**-
[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht, nicht im stabilen Core-Vertrag;
ihre feldgenauen Formen leben in den typisierten Schnittstellen des Produkts.

## Was es konsumiert & erzeugt

Das Modul **konsumiert** `cost.sampled` vom [Event-Bus](/de/reference/events/), um den
Katalog mit realer Pro-Token-Preisgestaltung und Nutzung anzureichern; es führt keinen neuen
Observation-Typ ein. Auf dem Pfad der gesteuerten Ausführung würde ein erfolgreicher Aufruf
ein bereinigtes `CostSample` an FinOps **erzeugen** — die Modellausgabe geht an den Aufrufer,
wird hier aber nirgends persistiert. Geld erscheint niemals auf dieser Oberfläche: Es wird
kein USD-Betrag zurückgegeben, nur Token-Zählungen und das Ziel, das bedient hat.

:::caution[Ehrliche Grenzen]
- **Routing-only Aktuierung.** Das Modul **löst** eine Route auf (Primär- + Fallback-Kette),
  **führt aber den Modellaufruf nicht aus**. Der Pfad der gesteuerten Ausführung ist eine
  **deny-closed Naht**: Ohne bereitgestellten Executor liefert er einen klaren `503` — die
  Control Plane kann ein Modell *auswählen*, wird aber nicht gegen einen Provider *ausgeben*.
  Wenn ein Executor verkabelt ist, verweigert ein FinOps-Budget an seiner Obergrenze die
  Ausgabe *vor* jedem Provider-Aufruf.
- **Deklarierte Preise sind ein Default, keine Garantie.** Listenpreise sind
  operator-verifizierte Defaults, mit einem Datum gestempelt; die maßgebliche Kostengröße
  realer Nutzung ist immer das konnektor-abgeleitete `CostSample`, niemals die bequeme
  Pro-Token-Zahl. Nicht zugeordnete Familien werden ohne Preis angezeigt — niemals mit einem
  erfundenen Preis.
- **Frisch angekündigte Modelle werden gelistet, aber gekennzeichnet.** Ein Preview-Modell,
  dessen Capabilities noch nicht gegen eine Model Card verifiziert sind, wird mit seinem
  Capability-Set als *to-confirm* markiert und ohne Preis katalogisiert, statt die Daten zu
  erfinden.
- **Das Key-Inventar sind Metadaten, niemals ein Secret.** Das Modul persistiert
  Governance-Beziehungen und einen maskierten Hinweis; der Credential-Wert verlässt niemals
  die Admin-API des Providers und wird niemals gespeichert. Manche Provider legen überhaupt
  kein Key-Inventar offen — eine dokumentierte Grenze, kein Versäumnis.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul X sitzt und sein Aktuierungsstatus.
- [Access & Resource Map](/de/reference/modules/iii-access-map/) — die R/RW-Map und der Least-Privilege-Drift.
- [Referenz Event-Bus](/de/reference/events/) — das `cost.sampled`-Event, das dieses Modul konsumiert.
- [Architekturüberblick](/de/explanation/architecture/overview/) — Engine, Schichten und Konnektoren.
- [Steuern und freigeben](/de/how-to/govern-and-approve/) — Handeln bei Routing und Governance.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — der Vertrag observe-broadly / actuate-on-a-subset.
