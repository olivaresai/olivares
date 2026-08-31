> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0022: Source-Scoping nach Subject-Achse (Session / Agent / Benutzer / Benutzergruppe / Rolle), mit Effect auf Zeilenebene und versionierter, dual kontrollierter Enforcement-Haltung

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## Kontext und Problemstellung

Das Source-Binding (`modules/sourcescope`) bindet eine verbundene Quelle — einen
MCP-Server, ein Modell, einen Provider, eine Wissensbasis, eine Datenquelle — an genau einen
von drei **Containment**-Scope-Bäumen: `workspace`, `agent_group` oder `folder`
(`schema.go:52-62`, `binding.go:33`). Es beantwortet: „Ein Akteur **in** diesem Scope darf
diese Quelle erreichen.“

Die Produktvision erfordert vier weitere Achsen, die das Containment-Modell nicht
ergonomisch ausdrücken kann:

- **„Diese SESSION sieht Quelle X“** — eine einzelne laufende Session.
- **„Dieser BENUTZER / diese Benutzergruppe greift auf Quellen Y zu“** — ein benannter
  Mensch und eine Verzeichnisgruppe von Menschen.
- **„Dieser konkrete AGENT (nicht seine Gruppe) sieht nur Z“** — ein Agent, nicht die
  Agentengruppe, zu der er gehört.

Heute werden diese Achsen lediglich durch das Verfassen eines rohen Cedar-Grants
*angenähert* — keine Binding-Ergonomie, keine auflistbare/auditierbare Zeile, keine
Access-Map-Projektion und (für die umgekehrte Frage „Welche Quellen kann Subject S
erreichen?“) das ungelöste Reverse-Query-Problem (`accessmap.go:44`). Gleichzeitig besitzt
die **Modell**-Governance bereits ein reiches SUBJECT-Modell —
`subject_kind ∈ {user, role, agent_group}` mit Allow-/Forbid-Zeilen und einer
`forbid-overrides-allow`-Algebra (`modelgovernance.go:98-100`,
`modelaccessgate.go:204`). Es besteht eine Governance-Asymmetrie: **Modelle werden umfassend
nach Subject gesteuert, Quellen nur eingeschränkt nach Containment.** Diese Entscheidung
schließt sie.

Eine Anforderung zweiter Ordnung ergibt sich aus der Analyse der etablierten Anbieter
(gegen Anbieterdokumentation am 2026-07-07 verifiziert): AWS Q Business macht das *Lockern*
einer ACL zu einer eigenen, einmaligen, auditierten IAM-Operation
(`qbusiness:DisableAclOnDataSource`); Googles Data-Store-ACL-Haltung ist **nach Erstellung
unveränderlich**. Unser Differenzierungsmerkmal ist eine **veränderliche, versionierte und
auditierte** Haltung — ihre Lockerung muss jedoch eine **privilegierte, dual kontrollierte,
auditierte** Operation sein, niemals ein stiller Schalter. Kein etablierter Anbieter drückt
Source-Scoping je Agent oder je Session aus — dies ist verifizierter White Space, keine
Hypothese.

## Entscheidungstreiber

- **Konsistenz mit Model-Access.** Dasselbe Subject-Vokabular und dieselbe
  `forbid-overrides-allow`-Algebra, damit Operatoren über „Wer darf eine Quelle erreichen?“
  genauso nachdenken wie über „Wer darf ein Modell verwenden?“.
- **Hot-Path-Kosten.** Der Resolver läuft auf dem EXECUTE-Pfad von Models (`ScopeGate`) und
  dem Knowledge-Retrieval-Pfad (`RetrievalScopeGate`). Die Identitätsachsen dürfen keinen
  Policy-Roundtrip je Resolve hinzufügen.
- **Auditierbarkeit und Reverse Query.** „Alle Quellen auflisten, die Session S / Benutzer U
  / Gruppe G zugeordnet sind“ muss eine einzelne indexierte Query sein, kein Cedar-
  Reverse-Walk (Reverse Query ist nicht gelöst).
- **UI.** Eine Binding-Form, die die Konsole (ein Follow-up) darstellen und verfassen kann.
- **Rückwärtskompatibilität und Sicherheit.** Ein Deployment ohne neue Bindings entscheidet
  genau wie zuvor; die Identitätsachsen müssen nach Möglichkeit an den
  **authentifizierten Principal**, nicht an eine vom Aufrufer deklarierte Zeichenfolge,
  gebunden sein; die Control Plane darf keine zweite Autorisierungs-Engine erhalten, damit
  die Angriffsfläche klein bleibt.

## Entscheidungsergebnis

**Das bestehende Source-Binding in-place erweitern (Kandidat A1): Subject-Scope-Bäume und
einen `effect` auf Zeilenebene ergänzen, sodass `sourcescope` eine Subject-scoped
Allow-/Forbid-Algebra über seiner eigenen Tabelle erhält, die Model-Access spiegelt — das
Containment-Modell und den Cross-Scope-Cedar-Override dabei exakt unverändert lassen.** Für
die neuen Achsen kein rohes Cedar verfassen (Kandidat B) und keine parallele
Model_access-Zwillings-Entscheidungsebene aufbauen (Kandidat C). Eine Control Plane, eine
Query-Oberfläche, eine Stelle, an der die Autorisierung korrekt sein muss.

### 1. Fünf neue Subject-Scope-Bäume, einheitlich mit den bestehenden Containment-Bäumen

`scope_tree` wächst von `{workspace, agent_group, folder}` und umfasst zusätzlich:

| tree | `scope_ref` | stimmt überein, wenn … | Identitätsquelle | fälschbar? |
|---|---|---|---|---|
| `session` | Session-`external_id` | handelnde Session == ref | session-bewusste Aufrufer-Ref, durch Agentenidentität gehärtet | route-gegatet (siehe §4) |
| `agent` | Agent-`external_id` | handelnder Agent == ref | `principal.AgentIdentity` ∨ Agent der Session ∨ Agenten-Ref | route-gegatet / authentifiziert |
| `user` | Benutzer-ID | `principal.UserID` == ref | **authentifizierter Principal** | nein |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **authentifizierter Principal** (durch Verzeichnisgruppe gegateter verschachtelter Abschluss) | nein |
| `role` | Mandantenrollenname | `principal.RoleIn(tenant)` == ref | **authentifizierter Principal** | nein |

`user_group` ist die **Verzeichnisgruppe** — Abgleich per **Gruppen-ID** mit
`principal.GroupsIn(tenant)`, das bereits auf dem authentifizierten Principal mitgeführt
wird und bereits den vollständigen verschachtelten Vorfahrenabschluss einbezieht
(`principal.go:67-77,151-164`); pro Resolve wird kein Group-Read hinzugefügt. `UserGroup`
hat keinen Slug (`model/auth.go:122`), daher ist die ID der stabile Identifikator. `role`
wird für **vollständige Model-Access-Parität** ergänzt (Fran Olivares, 2026-07-07): Eine
Quelle nach Mandantenrolle zu steuern ist der grobe „Benutzergruppen“-Hebel, den
Model-Access ebenfalls bereitstellt.

Die drei Individualidentitätsachsen (`session`, `agent`, `user`) sind degenerierte
Containments (Gleichheit); `user_group` und `role` sind echte Mitgliedschaften. Alle werden
als einheitliches **Scope-Prädikat** über dem Akteur ausgewertet — keine neue
Entscheidungs-Engine.

**Die Validierung folgt einer Containment-vs.-Subject-Dichotomie (verifizierte
Einschränkung).** Die Write-Handler des Moduls besitzen einen Business-Tenant-`store.Scope`;
die Auth-Subjects (`model.User`, `model.UserGroup`, Rollen) liegen im `store.AuthScope` (dem
Systemmandanten) und sind von dort **nicht erreichbar** (`core/store/store.go` gegenüber
`auth.go:24-36`). Daher:

- **Containment-Bäume** `workspace` / `agent_group` / `folder` **und** die im Store
  vorhandenen Subject-Bäume werden beim Binden wie heute auf Existenz geprüft (deny-closed,
  „kein hängender Scope“) — um jedoch eine einheitliche Regel zu behalten und das Binden
  einer Quelle vor einer ephemeren Session zu unterstützen, behandelt diese Entscheidung
  **alle fünf Subject-Bäume beim Authoring nur nach Form**: eine nicht leere `scope_ref` der
  richtigen Art, ohne Store-Lookup.
- Korrektheit hängt nicht von der Existenzprüfung ab: Eine unbekannte Subject-Ref stimmt
  einfach nie mit dem authentifizierten Akteur beim Resolve überein ⇒ deny-closed —
  **genau das Model-Access-Muster** (`modelaccessgate.go` validiert nur die *Form* des
  Subjects; `validateGrantRefs` prüft nur das im Store vorhandene TARGET). Tippfehlervermeidung
  ist Aufgabe der Konsole (Authoring über einen Verzeichnis-/Agenten-Picker), nicht der
  Binding-Schicht. Die Containment-Bäume behalten ihre bisherige Existenzvalidierung
  unverändert bei.

### 2. Ein `effect` auf Zeilenebene (allow | forbid), mit absolutem **forbid-overrides-allow**

Jedes Binding führt `effect ∈ {allow (default; empty stored value = allow), forbid}` (dieselbe
Konvention wie `normalizeEffect` von Model-Access). Die Resolver-Algebra wird für ein
`(actor, source)`:

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**Verhaltensänderung, dokumentiert (wie ADR-0019 seine eigene dokumentierte).** Heute gilt
das Forbid des Source-Bindings *je Binding*: Ein Cross-Scope-`EffectForbid` auf einem
Binding wird per `continue` übersprungen, und ein *anderes* Binding kann weiterhin erlauben
(`resolver.go:243-248`). Diese Entscheidung macht **alle** Forbids **absolut**
(`effect=forbid` auf Zeilenebene und Cross-Scope-`EffectForbid` gleichermaßen): Jedes
passende Forbid verweigert die Quelle und überschreibt Containment, Cross-Scope-Grant
**und** Tenant-RBAC — exakt die Algebra von Model-Access (`modelaccessgate.go:204`) und des
Cedar-Kerns (`EffectForbid` „ÜBERSCHREIBT alles“, `authorizer.go:101`). Die Richtung ist
strikt sicherer (ein Forbid kann ausschließlich verweigern), und kein bestehender
Single-Binding-Forbid-Test regressiert; sichtbar ist die Änderung nur im zuvor
unspezifizierten Multi-Binding-Fall.

**Confinement-Auslöser.** Eine Quelle ist genau dann *confined*, wenn sie ≥1 aktiviertes
**Allow**-Binding hat. Vorhandene Bindings sind sämtlich Allows, daher entspricht dies dem
heutigen „gebunden ⇔ hat Bindings“. Eine Quelle mit **ausschließlich Forbids** bleibt global,
außer für die darin benannten Subjects — die Model-Access-Haltung „bestimmte Subjects
einschränken“, nun auch für Quellen. Das Connector-Assignment-Gate basiert auf „kein
Allow-Binding“ (zuvor „kein Binding“), sodass eine reine Forbid-Quelle Connector-Assignments
weiterhin berücksichtigt.

### 3. Präzedenz: Forbid absolut; Credential nach spezifischstem Allow

Forbid ist absolut (§2), daher entscheidet Präzedenz niemals Allow gegen Deny — sie
entscheidet, **welches Credential** ein zugelassener Akteur erhält, wenn mehrere
Allow-Bindings passen. Die Reihenfolge von am spezifischsten zu am wenigsten spezifisch:

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

Zuerst Individualidentität, dann Verzeichnisgruppe, RBAC-Rolle, Gruppe des handelnden
Agenten und schließlich Ressourcen-Containment. Diese totale Ordnung macht die
Credential-Auswahl deterministisch (ersetzt die lexikalische Sortierung von
`loadEnabledBindings`) und entspricht der dokumentierten Präzedenz
`session > agent > group > workspace`, verfeinert für die fünf Achsen.

### 4. Achsenverfügbarkeit gilt je Enforcement-Punkt — und wird ehrlich ausgewiesen

Der Resolver besitzt zwei Entry-Points mit unterschiedlichem Akteurskontext:

| Achse | `ResolveForSession` (Models `ScopeGate`, Runtime) | `ResolveForAgent` (Knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ Ref der handelnden Session | ❌ keine Session im Kontext → stimmt nie überein |
| agent | ✅ Agent der Session (Agentenidentitäts-Override) | ✅ Agenten-Ref (Agentenidentitäts-Override) |
| user / user_group / role | ✅ authentifizierter Principal | ✅ authentifizierter Principal |
| workspace / agent_group / folder | ✅ (bestehend) | ✅ (bestehend) |

Ein `session`-Binding auf einer Wissensbasis wird auf dem reinen Agenten-Retrieval-Pfad
**nicht** durchgesetzt, weil dort keine Session existiert — es wird nicht stillschweigend
„erlaubt“, sondern gehört schlicht nicht zum Scope dieses Akteurs; andere Bindings/Achsen
derselben Quelle gelten weiterhin. Diese Asymmetrie ist im Vertrag dokumentiert und nicht
verborgen. Die Achsen `session`/`agent` bleiben route-gegatet; vom Aufrufer beeinflusste
Referenzen werden durch den Agentenidentitätscheck gehärtet (`principal.AgentIdentity`
überschreibt eine vom Aufrufer deklarierte Ref). `user`/`user_group`/`role` binden an den
**nicht fälschbaren authentifizierten Principal** und sind deshalb die stärkeren Achsen.

### 5. Die Enforcement-Haltung ist veränderlich, versioniert, auditiert — und ihre Lockerung dual kontrolliert

Die *Haltung* einer Quelle ist die Menge ihrer aktivierten Bindings und Effects. Laut
Fran Olivares (2026-07-07, „robust ohne Duplizierung“): **`revision.go` und `approvals.go`
von Governance sind modulintern und von `sourcescope` NICHT wiederverwendbar** (verifiziert:
nicht exportierte Helper, eigene Entitäten, REST-Approval-Flow). Eine Abspaltung in
`sourcescope` wäre duplizierte technische Schuld; daher sind die Haltungskontrollen
**eigenständig** und verwenden die eine bereits vorhandene gemeinsame unveränderliche
Primitive — das Audit-Ledger:

- **Auditiert und versioniert über die Audit-Kette.** Jede Haltungsänderung zeichnet das
  Haltungs-**Delta** im append-only, hash-verketteten Audit-Ledger (ADR-0009) auf —
  `sourcescope.binding.*` für Create/Update/Delete (`auditBinding` wird um `effect`
  erweitert) und `sourcescope.posture.{propose,approve,reject}` für den dual kontrollierten
  Lebenszyklus. Das Ledger IST der unveränderliche, sequenzierte Versionsverlauf; eine
  eigene nummerierte Revisions-*Tabelle mit Rollback* wird bewusst NICHT ergänzt (sie würde
  `governance/revision.go` duplizieren). Die ausstehenden/entschiedenen
  **posture-request**-Zeilen sind der erstklassige, abfragbare Datensatz jeder *Lockerung*
  (wer schlug sie vor, wer genehmigte sie).
- **Dual kontrolliert nur in lockernder Richtung, eigenständig.** Eine Änderung, die
  potenziell **erweitert**, wer eine Quelle erreichen darf, ist eine *Lockerung*: Sie wird
  NICHT vom Akteur angewandt, sondern als ausstehender `sourcescope_posture_request`
  festgehalten und erst angewandt, wenn ein **ZWEITER, ANDERER** Principal sie genehmigt
  (der Check `proposer != approver` stellt Zwei-Personen-Integrität her) und der Genehmiger
  die Admin-Berechtigung `sourcescope:posture:admin` besitzt (Funktionstrennung vom
  Editor-Proponenten).

  > **Status-Nachtrag, 2026-08-07.** Die Aufzählung unten ist KORRIGIERT. Ursprünglich
  > nannte sie *ein Allow erweitern* und *ein Allow verschieben*, nannte **keine** einzige
  > Scope-Operation auf einem `forbid` und führte „auf spezifischeren Baum einschränken" unter
  > den gewöhnlichen Single-Actor-Writes — **ohne Qualifizierung nach Effect**. Der Code setzte
  > das getreu um: Ein `forbid`, das ein aktiviertes `forbid` blieb und nur die abgedeckte
  > Population wechselte, wurde sofort und von einem einzigen Akteur angewandt — während das
  > LÖSCHEN desselben Forbid zwei Personen verlangte. Das Zwei-Personen-Gate ließ sich durch
  > Bearbeiten statt Löschen umgehen. Die Umstellung der Klassifizierer auf Whitelists legte
  > drei weitere Lecks derselben Klasse offen: ein `allow`, das auf einen „spezifischeren“
  > Baum verschoben wird; das UMWANDELN des LETZTEN aktivierten `allow` in ein `forbid`; und
  > das Anlegen eines `allow` auf einer BEREITS eingegrenzten Quelle (das Anlegen wurde
  > überhaupt nicht klassifiziert). Die allgemeine Regel am Anfang dieses
  > Punktes hat sich nie geändert und autorisiert die Korrektur: Die Aufzählung war immer enger
  > als die Regel, die sie zu präzisieren vorgab.

  **Die Klassifizierer sind WHITELISTS.** Sie zählen die Writes auf, die den Zugriff
  nachweislich nicht erweitern können, und behandeln **alles andere — auch jede Form, die sie
  nicht erkennen — als Lockerung**. Eine Blacklist lockernder Formen leckt konstruktionsbedingt;
  diese leckte an vier Stellen. Drei waren Änderungen an einem bestehenden Binding — ein
  `forbid`, das seinen Scope verkleinert, ein `allow`, das auf einen „spezifischeren“ Baum
  verschoben wird, und das LETZTE aktivierte `allow`, das zu einem `forbid` wird; die vierte
  war das Anlegen, das gar nicht klassifiziert wurde. Die ersten beiden entstanden daraus, eine
  Scope-Operation mit der Polarität eines `allow` zu lesen. Die dritte daraus, den EFFECT der
  Zeile zu lesen und die EINGRENZUNG zu vergessen, die dieselbe Zeile trug: Eine Quelle ist nur
  eingegrenzt, solange sie ein aktiviertes `allow` hat — der Write, der sich als „diese Zeile
  kann von hier an nur noch verweigern“ liest, ist derselbe, der die Quelle global macht.

  **Ein `forbid` KEHRT DIE POLARITÄT jeder Scope-Operation UM — das ist die Falle.** Bei einem
  `allow` erreicht ein kleinerer Scope weniger Akteure: Verschärfung. Bei einem `forbid`
  SCHÜTZT er weniger Akteure: Alle, die er nicht mehr abdeckt, sind durch diesen einen Write
  nicht mehr verweigert.

  **Zwei Scopes sind nur vergleichbar, wenn sie DERSELBE Scope sind.** `specificityRank`
  (`resolver.go`) **ordnet Bäume zur Auswahl eines CREDENTIALS** unter passenden Allow-Bindings;
  es **ist keine Containment-Relation** und darf nie als solche verwendet werden. `role:admin`
  und `user_group:g1`, `workspace:eng` und `agent_group:core`, ein Ordner und sein Kind sind
  verschiedene POPULATIONEN, und keine enthält die andere — und ein Folder-Binding hat
  überhaupt keine Containment-Dimension (es reitet auf dem cross-scope Cedar-Grant). Auch Mitgliedschaft ist
  nicht fix: eine heute durch Zeilenlesen bewiesene Obermenge ist morgen keine mehr. Das
  Zertifikat für „dieser Write kann nicht erweitern" ist daher die **Identität des Scope und
  nichts Schwächeres**, und „ich kann diese beiden Scopes nicht vergleichen" löst sich zu
  *Lockerung* auf: Ein False Positive kostet eine Genehmigung zu viel, ein False Negative ist
  die Umgehung eines Zwei-Personen-Gates.

  **Lockerungen**, genau (`classifyCreate`/`classifyUpdate`/`classifyDelete`): ein aktiviertes
  **Forbid** löschen oder deaktivieren; `forbid→allow`; **jede Scope-Änderung an einem
  aktivierten Forbid** (sie hebt die Verweigerung für einen Teil seiner Population auf); ein
  Allow **aktivieren**; das **letzte** aktivierte Allow deaktivieren oder löschen (entgrenzt
  die Quelle → global); **jede Scope-Änderung an einem aktivierten Allow** — ob weiter, enger
  oder seitwärts; **das Anlegen eines Allow auf einer BEREITS eingegrenzten Quelle** (ein Grant
  für eine Population, die die Quelle nicht erreichen konnte); und die spezielle einmalige
  Operation **`POST /sources/disable-scoping`** (Spiegel von AWS
  `qbusiness:DisableAclOnDataSource`).

  **Verschärfende / neutrale** Änderungen sind gewöhnliche Single-Actor-Writes — auditiert,
  aber nicht gegatet: ein **Forbid** hinzufügen; `allow→forbid`; das **ERSTE** aktivierte Allow
  auf einer nicht eingegrenzten Quelle anlegen (es stellt die Quelle unter Governance — die
  größte Verschärfung des Moduls, bewusst ohne Gate, damit der sichere Schritt nie der teure
  ist); eine **deaktivierte** Zeile anlegen; ein geparktes **Forbid** aktivieren; ein **nicht**
  letztes Allow löschen oder deaktivieren; und eine Notiz-/Credential-Änderung, die Effect,
  Enabled und Scope unangetastet lässt (der Credential-Locator wählt, WELCHE Referenz ein
  bereits autorisierter Akteur erhält, nie OB er autorisiert ist). Eine Zeile, die vorher und
  nachher deaktiviert ist, erzwingt nichts — jeder Write darauf ist neutral.

  Diese Asymmetrie entspricht AWS (Lockern ist die privilegierte Operation) und übertrifft
  Googles unveränderliche Haltung: unsere ist veränderlich *und* governable. Endpoints:
  Lockernde Creates/Updates/Deletes werden über vorhandene `POST /bindings` und
  `PUT`/`DELETE /bindings/{id}` VORGESCHLAGEN (Antwort `202` mit ausstehendem Request);
  `POST /posture-requests/{id}/{approve,reject}` entscheidet;
  `GET /posture-requests` ist die Reviewer-Queue.

### 6. Die Access Map projiziert die neuen Ursprünge (ADR-0003)

`publishBindingEdges` projiziert die erlaubte Seite der RRW-Map. `EdgeObservation`
unterstützt bereits `OriginKind ∈ {agent, session, identity}`
(`sdk/model/observation.go:55`), daher projiziert jede der drei Individualidentitätsachsen
EINE Edge: ein `session`-Binding → eine Edge mit `session`-Origin (ein Binding je Session
erscheint als Edge **dieser** Session); `agent` → eine Edge mit `agent`-Origin; `user` →
eine Edge mit `identity`-Origin. Die GRUPPEN-Subject-Achsen (`user_group`, `role`) müssten
ihre MITGLIEDER aufzählen, um Edges zu projizieren — die Mitglieder sind jedoch
Auth-Scope-Entitäten (Verzeichnisgruppen, Benutzer), die vom Tenant-`store.Scope` des Moduls
nicht erreichbar sind. Daher werden sie, genau wie die Reverse-Grant-Projektion eines
Folder-Bindings (der Reverse-Query-Aufschub), **AUFGESCHOBEN**: protokollieren und nichts
projizieren. Forbid-Bindings projizieren nichts (ein Forbid ist keine erlaubte Edge).
Enforcement ist stets die Live-Entscheidung des Resolvers gegen den Live-Principal; die Map
ist Best-Effort-Drift-Beobachtbarkeit, und eine aufgeschobene/fehlende Edge schwächt sie nie.

## Konsequenzen

- **Gut:** Die vier Visionsachsen (fünf mit `role`) sind ausdrückbar, auf beiden echten PEPs
  deny-closed durchgesetzt und in Scope-Auflösung und Access Map sichtbar; eine
  auditierbare/auflistbare Binding-Form für die Konsole; Identitätsachsen binden an den
  authentifizierten Principal (nicht fälschbar); keine zweite Autorisierungs-Engine (kleine
  Angriffsfläche); der Hot Path zahlt einen günstigen Mitgliedschaftscheck und **null** neue
  Policy-Roundtrips für die Identitätsachsen; eine veränderliche, aber governte Haltung als
  verifiziertes Differenzierungsmerkmal gegenüber AWS (einmalig) und Google (unveränderlich).
- **Schlecht / Abwägungen:** `scope_tree` trägt nun sowohl „Containment-Scope“- als auch
  „Subject-Identitäts“-Semantik (abgemildert: der Vertrag fasst beide als einheitliches
  *Scope-Prädikat*); Haltungs-/Dual-Control-Mechanik vergrößert die reale Oberfläche, die
  ein minimales Deployment erst beim Verfassen einer Lockerung nutzt; Forbid absolut zu
  machen ist eine dokumentierte Verhaltensänderung (sichere Richtung).
- **Neutral:** `role` überschneidet sich konzeptionell mit dem bestehenden
  Tenant-RBAC-Soft-Isolation-Bypass (`rbacAllows`) — beide komponieren (ein `role`-Binding
  ist ein positiver Scope; der RBAC-Bypass ist die Sichtbarkeitsregel für Tenant-Operatoren),
  und ein Forbid überschreibt **beide**.

## Warum die Alternativen verworfen wurden

- **(B) Eine High-Level-API, die Cedar-Policies für die neuen Achsen generiert.** Abgelehnt:
  (1) Sie wäre die *einzige* Ebene, die rohes Cedar verfasst, während Model-Access — das
  Konsistenzziel — Cedar **nicht** generiert, sondern über eigene Zeilen entscheidet
  (`modelaccessgate.go:11-14`). (2) Sie verursacht je Resolve einen Cedar-Roundtrip auf dem
  Hot Path. (3) Die umgekehrte Konsolenfrage („Welche Quellen kann Subject S erreichen?“)
  ist die ungelöste Cedar-Reverse-Query, sodass UI und Access Map blockiert oder nur
  angenähert wären. (4) Für das Audit von „Wer hat was gescoped?“ müsste Policy-Text statt
  Zeilen gelesen werden.
- **(C) Eine separate Model_access-Zwillingstabelle für Source-Subject-Grants, komponiert mit
  dem bestehenden Containment-Binding.** Als Over-Engineering abgelehnt, das Robustheit
  *reduziert*: Zwei Entscheidungsebenen müssen an jedem PEP komponiert und konsistent
  gehalten werden — eine klassische Quelle für Security Drift (eine aktualisiert, die
  andere nicht; mehrdeutige ebenenübergreifende Präzedenz). „Am vollständigsten/Enterprise“
  entsteht durch **Tiefe auf einer Ebene** (alle Achsen + Effect + versionierte, dual
  kontrollierte Haltung + vollständige Testmatrix), nicht durch duplizierte Leitungen. Eine
  einzelne Control Plane mit einheitlicher Algebra ist leichter zu auditieren („alles, was
  Quelle X steuert“ = eine Query) und korrekt zu beweisen.
- **Das ScopeSpec-Vokabular benutzerdefinierter Rollen statt eines lokalen Enums erweitern.**
  Abgelehnt: `scope_tree` von `sourcescope` ist eine modullokale Konstante, die den
  Custom-Role-Katalog nur *spiegelt* (`schema.go:49`); die Erweiterung eines gemeinsamen
  Katalogs würde Source-Achsen in das durchsickern lassen, worauf Custom Roles zielen dürfen.
  Die neuen Bäume bleiben lokal in `sourcescope`.
