> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0013: Autorisierungs-PDP — Cedar eingebettet + OPA-over-HTTP-Adapter

- **Status:** accepted (restrict-only, beschränkt auf den in diesem Record geschaffenen
  `auth.PolicyEvaluator`-Seam) — **geändert durch ADR-0019 (2026-06-15)**, die das
  Basis-Permit entfernt: Eine Permit-Regel des Betreibers, die dieses Overlay stillschweigend
  neutralisierte, gewährt jetzt tatsächlich; außerdem wurde Cedar selbst in einem separaten
  Seam zu einer positiven, bereichsgebundenen Grant-Engine. Reine Forbid-Policies bleiben
  unverändert; die Formulierung „niemals erweitern“ im Kontext und in den
  Entscheidungstreibern ist in dieser Lesart überholt — siehe den Nachtrag am Ende.
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares
- **References:** NHI/MCP-auth contract; geändert durch ADR-0019 (Cedar Scoped Grants)

## Kontext und Problemstellung

Über RBAC hinaus benötigt die Plattform einen Policy Decision Point (PDP) für
attributbasierte Autorisierung. Organisationen unterscheiden sich: einige wünschen eine
autarke Engine, andere haben bereits ein bestehendes OPA-Estate. Der PDP darf den Zugriff
niemals *erweitern* — nur einschränken.

## Entscheidungstreiber

- Autark funktionieren (Single-Binary, Air-Gap) ohne externen Policy-Dienst.
- Zu einem bestehenden OPA-Deployment passen, wenn der Betreiber eines hat.
- Eine restrict-only-Invariante: Policy kann verweigern, niemals über RBAC hinaus gewähren.

## Betrachtete Optionen

- **Beides:** Cedar eingebettet (primär, pure-Go) **und** ein OPA-over-HTTP-Adapter hinter
  einem Seam, vom Betreiber ausgewählt.
- **Nur Cedar.**
- **Nur OPA.**

## Entscheidung

Gewählte Option: **beides, hinter einem einzigen `PolicyEvaluator`-Seam**. **Cedar** ist
der eingebettete, pure-Go primäre PDP; ein **OPA-over-HTTP**-Adapter ist verfügbar; der
Betreiber wählt die Engine über `OLIVARES_PDP_ENGINE = cedar | opa | none`. Der ABAC-Seam
**schränkt nur ein** (er verknüpft mit RBAC per AND und erweitert niemals). Die
restrict-only-Invariante wird end-to-end getestet.

### Konsequenzen

- **Gut:** standardmäßig autark (Cedar, kein Sidecar); passt zu einem OPA-Estate, wenn
  gewünscht; ein Seam, zwei Engines.
- **Schlecht / Kompromisse:** zwei Adapter zu pflegen; die Transport-Härtung des OPA-Pfads
  (z. B. mTLS zum Sidecar) ist eine dokumentierte Erweiterung, noch nicht vollständig.
- **Neutral:** `none` deaktiviert die ABAC-Schicht und belässt RBAC deny-by-default.

## Warum die Alternativen verworfen wurden

- **Nur Cedar** — schließt Organisationen aus, die auf OPA standardisiert sind.
- **Nur OPA** — erzwingt einen externen Policy-Dienst bei jeder Installation und bricht den
  autarken / Air-Gap-Standard.

## Nachtrag (2026-06-15, ADR-0019)

*(Die ändernde Entscheidung ist auf den 2026-06-15 datiert; dieser Hinweis wurde am
2026-08-17 hinzugefügt, nachdem eine Prüfung des Entscheidungsregisters festgestellt hatte,
dass die beiden im Abstand von elf Tagen unterzeichneten Records keinen Vorrangverweis
aufeinander enthielten. Der vorstehende Text wird nicht umgeschrieben.)*

**Was in der geschriebenen Form nicht mehr gilt.** Die Aussage im Kontext *„Der PDP darf
den Zugriff niemals erweitern — nur einschränken“* und der Treiber *„Policy kann
verweigern, niemals über RBAC hinaus gewähren“* lesen sich in ihrer geschriebenen Form
als Aussagen über **die gesamte Autorisierungsentscheidung** und über **Cedar**. Seit
**ADR-0019** stimmt in dieser Lesart keine von beiden: Cedar wurde von einem reinen
Deny-Overlay zu einer dreiwertigen, bereichsbewussten **Grant**-Engine erhoben, und das
implizite Basis-`permit`, das eine Cedar-Entscheidung restrict-only machte, wurde entfernt,
sodass ein verfasstes `permit` jetzt tatsächlich gewährt.

**Was stattdessen gilt.** Die restrict-only-Invariante besteht **beschränkt auf den Seam,
den dieser Record geschaffen hat**, fort: `auth.PolicyEvaluator` wird weiterhin nach RBAC
ausgeführt und darf nur weiter einschränken (`core/auth/authorizer.go:100-104`). Der positive
Grant liegt in einem **anderen, neuen** Seam, `auth.ScopedAuthorizer`, der **neben** dem
Deny-Overlay und nicht darin verdrahtet ist. Der Authorizer kombiniert beide als
`Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay` (`core/auth/authorizer.go:157-163`,
Algebra bei `:161` und `:200`). Deny-by-default, Forbid-overrides-permit und Fail-closed bei
Fehlern bleiben erhalten; ein Deployment ohne verfasste Grants entscheidet genau wie unter
diesem Record. Alles andere hier gilt weiter: die beiden Engines hinter einem Seam und der
Selector `OLIVARES_PDP_ENGINE = cedar | opa | none` sind das ausgelieferte Verhalten
(`cmd/olivares/wire.go:994-1018`).

**Wo die aktuelle Entscheidung steht.** `docs/adr/0019-cedar-scoped-grants.md` (accepted,
2026-06-15, Fran Olivares), die ausdrücklich auf diesen Record verweist. Wer nur diesen ADR
zitiert — denjenigen, zu dem der Begriff *ABAC* führt — kann schließen, dass der ausgelieferte
positive Grant-Pfad gegen eine unterzeichnete Entscheidung verstößt. Das tut er nicht; er
folgt ADR-0019.
