> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0023: Durchsetzung der Kontext-Policy an den drei Transitpunkten, mit Fenster- und Ausgabenobergrenzen je Gruppe

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## Kontext und Problemstellung

Die Kontext-Policy (Fenstergröße und Kompaktierungsstrategie) wurde als Governance-Daten
persistiert, aber **kein Consumer wandte sie jemals an** — der von einem Codekommentar
versprochene Consumer existierte nicht, daher war die Policy totes Metadatum. Separat davon
galten die Token-Obergrenzen des Inference-Proxys nur **je Mandant / je Anfrage**, und FinOps
führt eine Budgetdimension `team`, die **detektiv und fail-open** ist. Es gab keine
Möglichkeit festzulegen: „Diese Gruppe von Benutzern (oder Agenten) darf höchstens so viel
Fenster / so viele Ausgaben verbrauchen“, und dies tatsächlich durchzusetzen.

Die Produktvision verlangt zwei Dinge, die die gespeicherte, aber ungenutzte Policy nicht
leisten konnte:

1. **Die Kontext-Policy ENTSCHEIDET an allen drei Transitpunkten**, an denen die Plattform
   eine Modellanfrage berührt — Session-Runtime, Inline-Inference-Proxy und
   Wissens-Retrieval —, statt inerte Daten zu bleiben.
2. **Durchgesetzte Obergrenzen je Gruppe** — `user_group` und `agent_group` — sowohl für das
   **Kontextfenster** als auch für **Ausgaben**, deny-closed, wo die Policy dies verlangt,
   und mit **ehrlicher Degradation** (niemals stilles Begrenzen oder stilles Erlauben).

## Entscheidungstreiber

- **Konsistenz mit Source-Scoping (ADR-0022).** Dasselbe Subject-Vokabular und dieselbe
  `most-specific`-Präzedenz wiederverwenden, sodass Operatoren über Kontext-Governance
  genauso nachdenken wie über Source-Scoping — keine zweite Entscheidungs-Engine, kleine
  Angriffsfläche.
- **Eine Obergrenze muss tatsächlich eine Obergrenze sein.** Ein numerischer Grenzwert, den
  ein spezifischerer Scope *lockern* kann, ist keine Obergrenze; „durchgesetzte
  Obergrenzen“ sind der Kernpunkt.
- **Ehrliche Degradation.** Wo die Plattform etwas nicht vollständig abrechnen kann
  (ungefähre Gruppenausgaben), muss sie in die *sichere* Richtung fehlschlagen und dies
  ausweisen — niemals fälschlich verweigern, niemals stillschweigend erlauben.
- **Bestehende Primitive wiederverwenden.** Audit-Ledger, bestehende Kostenzuordnung je
  Subject und bestehenden Proxy-Deny-Pfad gegenüber neuer Querschnittsmechanik bevorzugen.

## Entscheidungsergebnis

### 1. `Apply`-Komposition — qualitativ most-specific, Security-Floors restriktiv, `max_tokens` per MIN

`Module.Apply` (`modules/knowledge/context.go:263`) löst die effektive Policy für eine
Anfrage auf:

- **Qualitative** Felder werden nach **most-specific-wins** (`strategy`) aufgelöst,
  konsistent mit ADR-0022.
- **Security-Floors** werden **restriktiv** komponiert: `forbid` ist absolut;
  `redaction_required` wird per OR komponiert; `excluded_sources` per Vereinigung.
- **`max_tokens` wird per MIN komponiert** (am restriktivsten; Feld in
  `context.go:62,73`, begrenzt in `context.go:124`). Dies ist die bewusste Verfeinerung für
  den numerischen Grenzwert: Eine Obergrenze, die ein spezifischerer Scope erhöhen könnte,
  wäre keine Obergrenze. Das Verhalten lässt sich in etwa zwei Zeilen umkehren, falls ein
  Deployment jemals most-specific für den Grenzwert bevorzugt.

### 2. Agentenidentität im Proxy — den erreichbaren Rest schließen (E3-lite), den Rest ehrlich aufschieben

Das Session-Inference-WIF-Credential (`sk-ant-oat`) durchläuft **nicht** den
Inline-Inference-Proxy, der nur die plattformeigenen Tokens `olvs` / `olvk` authentifiziert.
Die Agentenidentitätsföderation für *Session*-Traffic vollständig zu schließen, würde eine
Neugestaltung des Inference-Credentials erfordern (mehrere Tage, Teil der Haltung für
ephemeres WIF-Minting) und wird **auf einen eigenen Aufwand (E3-full) verschoben**.

Der erreichbare Teil wird jetzt geschlossen (**E3-lite**): `authToken` propagiert `AgentRef`
→ `AgentIdentity`, und der Actor-Scope-Resolver von Models berücksichtigt den
**authentifizierten Principal** statt eines vom Aufrufer deklarierten Werts (Bugfix). Dadurch
wird die Achse `agent_group` im Proxy für Agent-on-behalf-of-Aufrufer ermöglicht. Die
Agentenreferenz stammt immer aus dem authentifizierten Credential, nie aus dem Request-Body
(`context.go:278-279`, `query.go:110-111`).

### 3. AUSGABEN-Obergrenze je Gruppe — präventiv, von Natur aus fail-open, mit granularer fail-closed-Option

Budget erhält die Dimensionen `user_group` / `agent_group`, die **präventiv** über
`CheckBudget` durchgesetzt werden. Die Ausgaben einer Gruppe werden per **Member-Fan-out**
über die bestehende Kostenzuordnung je Subject summiert (es gibt keine Gruppenspalte; jede
Zeile unterschiedslos zu summieren wäre ein Fehlzuordnungsfehler —
`modules/finops/ingest.go:75,361`).

Die Haltung ist **fail-open** — entsprechend der Natur einer Budgetprüfung und der
Produktaufteilung *Security = deny-closed* gegenüber *Budget = fail-open*
(`modules/models/api.go:639,656`) — mit einem **`fail_closed`**-Schalter je Budget für
Deployments, die einen harten Stopp wünschen (`modules/finops/budgets.go:102,166,182`). Dies
wird **ehrlich** ausgewiesen: Präventive Gruppenausgaben sind *ungefähr*, keine exakte
Abrechnung. Die Abdeckung wächst mit der Zuordnung — noch nicht zugeordnete Ausgaben
unterzählen die Gruppe lediglich; das ist die sichere Richtung (sie verweigert nie
fälschlich). Der detektive FinOps-Ingest-/Finding-Backstop für Gruppen und lokale
Degradationszähler sind ein **dokumentiertes Follow-up**, bewusst nicht halb verdrahtet.

### 4. Proxy-Verweigerung über dem Fenster — 413, niemals den Client-Payload verändern

Überschreitet eine Anfrage das effektive Policy-/Gruppenfenster, **verweigert der Proxy mit
HTTP 413** und einem Detail (`cmd/olivares/inferenceproxy.go:449`); er **verändert niemals
den opaken Payload des Clients** — er verweigert, statt still zu begrenzen
(`inferenceproxy.go:550`). Kompaktierung und signalisierte Kürzung finden nur dort statt, wo
die Plattform den Kontext selbst zusammensetzt (Retrieval und Session-Runtime), niemals am
Prompt des Aufrufers. Es gibt keine stille Degradation.

Die drei Enforcement-Punkte sind verdrahtet: Retrieval (`modules/knowledge/query.go:167` →
`:354`), Session-Runtime (`modules/sessions/runtime.go:285,623`) und Inference-Proxy (oben).

## Entscheiden und festhalten (innerhalb der genehmigten Richtung)

- **Neun Scope-Arten der Kontext-Policy** —
  `session > agent > user > user_group > role > agent_group > kb > workspace > tenant` — werden am Write-Handler validiert
  (`modules/knowledge/context.go:102-103`), mit einem nullable, expand-only `effect` (ein
  etablierter Abgleich einer Modulspalte, keine nummerierte Migration).
- **`surface` und `model` sind keine Scope-Arten.** Retrieval hat keine Surface, und der
  Proxy faltet das Fenster je Surface bereits in MIN ein; ihre Ergänzung wäre daher
  ungenutzte Allgemeinheit (YAGNI).
- **„OTel-Metrik“ für diese Funktion = auditierbare Ereignisse + native Findings**, kein
  modulinterner Zähler. Produkttelemetrie fließt als Findings über den Bus in
  Observability; ein neuer Zähler wäre eine Querschnittsänderung der Architektur und hier
  nicht im Scope.

## Betrachtete Alternativen

- **Most-specific-Komposition für `max_tokens`** (einheitlich mit den qualitativen Feldern):
  abgelehnt — eine numerische Obergrenze, die ein spezifischerer Scope erhöhen kann, ist
  keine Obergrenze und verfehlt das Ziel. Bleibt trivial umkehrbar, falls ein Deployment
  anderer Meinung ist.
- **Ein dedizierter modulinterner Zähler für Kontext-/Gruppentelemetrie:** als
  Querschnittsänderung der Architektur abgelehnt; der Pfad aus Audit-Ereignissen +
  Bus-Findings trägt das Signal bereits.
- **Alle Ausgabenzeilen je Subject für eine Gruppe ohne Member-Fan-out summieren:**
  abgelehnt — das überzählt und ordnet falsch zu; Fan-out über die authentifizierte
  Mitgliedschaft ist die korrekte, sichere Zuordnung.

## Konsequenzen

- Die Kontext-Policy wird von totem Metadatum zu einer **aktiven Entscheidung** bei
  Retrieval, Proxy und Session-Runtime.
- **Fenster**-Obergrenzen je Gruppe sind **hart und per MIN komponiert**;
  **Ausgaben**-Obergrenzen je Gruppe sind **präventiv und ehrlich ungefähr**, mit einem
  opt-in `fail_closed`.
- **Registrierte Schuld, nichts halb verdrahtet:** E3-full (Session-Inference über eine
  governte Identität neu routen), der detektive Gruppenausgaben-Backstop über FinOps plus
  lokale Degradationszähler und das Durchreichen des Principals (`user` / `user_group`) an
  das Launch-Gate. Alles sind dokumentierte Follow-ups.
