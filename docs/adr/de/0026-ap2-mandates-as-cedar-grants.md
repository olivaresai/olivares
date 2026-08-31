> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0026: AP2-Zahlungsmandate als Cedar-scoped Grants (governte Beschaffung)

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## Kontext und Problemstellung

Agentische Zahlungen kommen als Protokollschicht an. Googles **AP2 (Agent Payments
Protocol)** ist eines der sichtbarsten; seine aktuelle Spezifikation ist **v0.2.0
(veröffentlicht 2026-04-28)**, und es wurde am selben Tag an die FIDO Alliance übergeben. AP2
erlaubt es einem Benutzer, ein signiertes **Mandat** an einen Shopping-Agenten zu delegieren,
das der Agent später an eine konkrete Transaktion bindet, die **Verifier** (Händler,
Credential-Provider, Netzwerk, Zahlungsabwickler) prüfen.

Zwei Fakten legen die Form dieser Entscheidung fest:

1. **Aktualität (die gemessene Realität schlägt den Plan).** Frühere Planungen stützten sich
   auf AP2 v0.1 und beschrieben ein Mandats-Tripel *Intent / Cart / Payment*, signiert mit
   „Verifiable Credentials“. Dieses Modell ist **überholt**. v0.2 definiert genau **zwei**
   Mandatstypen — **Checkout Mandate** und **Payment Mandate** — jeweils in einem
   **Open**-Zustand (Constraint-tragend, benutzersigniert) und einem **Closed**-Zustand
   (transaktionsgebunden; der Agent erzeugt ein Key Binding JWT / einen Proof-of-Possession
   über den Schlüssel im `cnf`-Claim des offenen Mandats). Mandate sind **SD-JWTs**
   (RFC 9901); der **Binding-Hash / das Key Binding JWT MUSS ein nicht-deterministisches
   Verfahren (ES256/ECDSA) verwenden und NICHT ein deterministisches (Ed25519)** — laut
   Spezifikation schützt dies die Hash-Bindung. Dieser ADR zielt auf **v0.2** und ist an die
   veröffentlichten `vct`-Schema-Suffixe gepinnt (gemäß der v0.2-Spezifikation
   `mandate.checkout.1` / `mandate.payment.1`; zur Build-Zeit gegen `docs/ap2/*` der
   Spezifikation verifizieren).

2. **Was Olivares ist — und was nicht.** Olivares ist eine **Governance-Control-Plane**: ein
   Policy Decision Point (PDP) und ein manipulationstransparentes Evidenz-Ledger. Es
   ist **kein** Zahlungsabwickler, PSP, Kartennetzwerk, keine Wallet und kein Verwahrer von
   Geldern, und dieser ADR macht es auch nicht dazu. AP2 selbst ist **pre-1.0** mit **früher,
   weitgehend aspirationaler Adoption** (PayPals eigene Seiten erwähnen AP2 nur taxonomisch
   und betonen OpenAIs ACP + Googles UCP; Mastercards „Agent Pay“ ist ein eigenes Programm;
   die Zahl „60+ Organisationen“ ist ein Launch-Stand von Sept. 2025; die
   FIDO-Unterzeichnerliste umfasst ~12). Ehrliche Kennzeichnung verbietet es,
   AP2-Unterstützung über das hinaus zu behaupten, was verifizierbar ist.

Das Problem: **Wie steuert Olivares einen AP2-vermittelten agentischen Kauf mit den
Primitiven, die es bereits hat, geboren mit einem konkreten Enterprise-Anwendungsfall und die
Lücken abdeckend, die AP2 bewusst der darüberliegenden Schicht überlässt — ohne einen
Autorisierungs-Fall-through oder eine stille Abschwächung eines Constraints einzuführen?**

Der konkrete Anwendungsfall, mit dem dieser Entwurf geboren wird: ein **governter
Beschaffungsagent** — ein Unternehmen kauft über einen Agenten, der unter einem offenen
AP2-Mandat arbeitet, dessen Constraints die Einkaufspolitik kodieren (Budgetobergrenze,
zugelassene Lieferanten, Limits je Position, Wiederholung, Ausführungsfenster); Olivares
autorisiert jeden konkreten Kauf gegen diese Policy, eskaliert die Käufe mit hohem Wert an
einen Menschen und versiegelt Mandat+Receipt als nicht abstreitbare Evidenz.

**Vorbedingung (In-Path-Gate).** Jede Garantie unten gilt nur dort, wo das Deployment den Kauf
**durch Olivares als In-Path-Gate** routet — der Agent MUSS eine frische
Olivares-Autorisierung einholen, bevor er ein geschlossenes Mandat der Settlement-Schicht
vorlegt. Als beiseitestehender/beratender PDP erreicht Olivares ein bereits an einen Händler
übergebenes geschlossenes Mandat ebenso wenig wie AP2 es erreichen kann. Der Build MUSS diese
Deployment-Anforderung dokumentieren.

## Entscheidungstreiber

- **Die bestehende Autorisierungsebene wiederverwenden, nicht forken** — aber nur dort, wo die
  Semantik tatsächlich passt (siehe die Korrektur Abstain-vs.-Deny unten).
- **AP2s benannte Lücken auf unserer Schicht abdecken** (siehe die begleitende
  Threat-Model-Spezifikation): AP2 hat **keinen Widerruf**, macht die Double-Spend-Ablehnung
  auf Verifier-Seite **optional (MAY)**, beweist **nicht** die menschliche Identität / SCA,
  **schweigt zum Vertrauen in die Uhr** und lässt Aufbewahrung/Abruf von Evidenz sowie Haftung
  außerhalb des Scopes. Ein PDP, der „annimmt, dass alle Agenten potenzielle Angreifer sind“
  (AP2s eigenes Threat-Model), muss diese verpflichtend machen.
- **Fail-closed bei allem, was nicht modellierbar ist.** Ein Constraint, das wir nicht
  kodieren können, eine Disclosure, die der Agent zurückhält, ein unbekannter Algorithmus —
  jedes davon muss das Mandat ablehnen, es niemals erweitern.
- **Ehrlicher Scope und pre-1.0-Risiko.** Jetzt entwerfen, an `vct` pinnen, keine
  Behauptungen ausliefern, die wir nicht verifizieren können, Olivares strikt auf der
  PDP-/Evidenzseite der Linie halten.

## Betrachtete Optionen

- **Option A — AP2-Mandate als Cedar-scoped Grants; Olivares als steuernder Verifier/PDP.**
  Ein **offenes AP2-Mandat** als verfassten **Cedar-Grant** (ADR-0019) modellieren, der an
  genau dieses eine Mandat gebunden ist und dessen `when`-Bedingungen die Mandats-Constraints
  sind; ein **geschlossenes Mandat** als **Autorisierungsanfrage** behandeln (Principal = der
  Agentenschlüssel in `cnf`; Action = `purchase`/`pay`; Resource = der Zahlungsempfänger / das
  Checkout), ausgewertet **deny-by-default für Zahlungs-Actions**. Olivares führt die
  Verifikationsregeln von AP2 als PDP aus, gatet die Käufe mit hohem Wert über die
  Einmal-HITL-Genehmigung, reserviert FinOps-Budgets (ADR-0025) fail-closed und versiegelt das
  vollständige signierte Mandat+Receipt als Evidenz.
- **Option B — eine maßgeschneiderte AP2-Mandats-Engine parallel zu Cedar.**
- **Option C — nur beobachten.**

## Entscheidungsergebnis

Gewählte Option: **Option A**, weil das Constraint-Modell auf Cedar-Grant-Bedingungen abbildet
und die umgebenden Kontrollen (Genehmigungen, Reserve-Ledger, signierte Audit-Kette) bereits
existieren — **vorausgesetzt, die drei semantischen Korrekturen unten werden vorgenommen**,
ohne die die Wiederverwendung unsicher ist.

### Die drei semantischen Korrekturen, die die Wiederverwendung tragfähig machen

1. **Zahlungs-Actions sind DENY-BY-DEFAULT, nicht Abstain-fällt-zurück-auf-RBAC.** Die
   Scoped-Grant-Engine liefert **`EffectAbstain`** (nicht Deny), wenn kein Permit passt —
   „kein Grant“, „abgelaufener Grant“ und „keine scoped Grants für den Mandanten“ ergeben
   allesamt Abstain, und Abstain bedeutet, dass *die Basis-RBAC-Entscheidung Bestand hat*
   (`modules/governance/grants.go:31-38`, das RBAC-Rückwärtskompatibilitäts-Invariant). „Kein
   passendes Mandat“ naiv mit „Deny“ gleichzusetzen ist **falsch**: Eine cnf-Abweichung, ein
   abgelaufenes Mandat oder ein widerrufener Grant würden Abstain ergeben und könnten auf ein
   **RBAC-Allow** durchfallen. Korrektur: `purchase`/`pay` werden **ausschließlich** durch
   einen passenden, gültigen, mandatsgebundenen Grant autorisiert, **ohne RBAC-Fallback**. Der
   Build MUSS dies erzwingen, entweder (i) indem er nachweist, dass der Basis-Authorizer
   keiner Rolle ein `purchase`/`pay`-Permit gewährt (sodass Abstain→Deny), oder (ii) durch ein
   Zahlungs-Overlay, das Abstain bei einer Zahlungs-Action als Deny behandelt. Ein
   vorhandenes, aber ungültiges Mandat verfasst zusätzlich ein explizites **`forbid`**. Ein
   Konformitätstest MUSS sicherstellen, dass RBAC allein niemals eine Zahlung autorisiert.

2. **Der Mandat→Grant-Translator IST FAIL-CLOSED bei jedem nicht modellierbaren Constraint.**
   „Unbekanntes Constraint MUSS fehlschlagen“ ist eine Verpflichtung **zur Übersetzungszeit**
   und nichts, was Cedars Deny-by-default liefert: Lässt der Translator ein Constraint, das er
   nicht kodieren kann, stillschweigend weg, erzeugt er einen Grant, der **breiter ist, als
   der Benutzer signiert hat**, und Cedar erlaubt, weil es das Constraint nie gesehen hat.
   Korrektur: gegen eine **Allowlist** erkannter Constraint-Schlüssel, -Operatoren und
   -Einheiten übersetzen; bei jedem nicht erkannten Element **das gesamte Mandat ablehnen und
   keinen Grant verfassen**.

3. **Vollständige Disclosure ist verpflichtend; der nicht vertrauenswürdige Agent kann kein
   Constraint zurückhalten.** In SD-JWT wählt der *Holder* (der nicht vertrauenswürdige Agent)
   aus, welche Disclosures er offenlegt. Er könnte nur die Disclosures vorlegen, die durchgehen,
   und ein strengeres Constraint zurückhalten. Korrektur: Der Verifikationsadapter zählt die
   `_sd`-Digests auf; ist ein Digest für einen policy-relevanten Claim **nicht offengelegt**,
   behandelt er ihn als nicht auswertbares Constraint und **schlägt fail-closed fehl**.

### Korrespondenz (mit angewandten Korrekturen)

| AP2-v0.2-Konzept | Olivares-Primitive (file:line) |
|---|---|
| Offenes Mandat (Constraints, benutzersigniert) | Cedar-scoped **Grant**, gebunden an `jti`/`sd_hash` dieses Mandats (`modules/governance/grants.go:67`, ADR-0019) |
| Geschlossenes Mandat | Autorisierungs-**Anfrage**, ausgewertet **deny-by-default für `purchase`/`pay`** (Korrektur 1) |
| „Verification and Processing Rules“ | Adapter-Kettenverifikation + Full-Disclosure-Prüfung (Korrektur 3) + fail-closed Übersetzung (Korrektur 2) + PDP-Entscheidung |
| `payment.budget` (kumulativ) / `amount_range` (je Transaktion) | FinOps-Reserve-Ledger (`modules/finops/budgets.go`, `spendlimits.go`, ADR-0025) mit einem **komplett neuen Reservierungsschlüssel je Mandat**; atomar gegen die Mandatsobergrenze UND alle Olivares-Scopes reservieren (NICHT `min()`) |
| `payment.agent_recurrence` (Anzahl/Velocity) | **Komplett neuer** Anzahl-/Velocity-Limiter (TOCTOU-sicher unter ADR-0025) — KEIN bestehendes betragsbasiertes Budget |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Cedar-`when`-Bedingungen auf Mengenzugehörigkeit |
| `execution_date` {not_before,not_after} | Zeitliche Bedingung gegen die **vertrauenswürdige signierte DDIL-Totmann-Uhr** (`modules/governance/ddiladopt.go`), auch in den SD-JWT-Adapter injiziert |
| Benutzergenehmigung; Gating bei hohem Transaktionswert | Verbrauch einer **Einmal-HITL**-Genehmigung (`modules/governance/approvals.go`) |
| Checkout/Payment Mandate + Receipt (Streitfall-Evidenz) | Hash-verkettetes **Runtime-Audit-Ledger** mit `transaction_id` als Schlüssel (`modules/sessions/runtime_ledger.go`, `sc.Audit().Append`, ADR-0009) — siehe Entscheidung 1 dazu, WAS gespeichert wird |

### Die Entscheidungen, die dieser ADR trifft

1. **Mandatsdarstellung — Autorität und Evidenz sind getrennte Stores.**
   - **Autorität** ist der **Cedar-Grant** (die ausgewertete Policy), gebunden an die stabile
     ID (`jti`/`sd_hash`) des konkreten offenen Mandats, sodass ein geschlossenes Mandat nur
     gegen den Grant ausgewertet werden kann, der aus *seinem* offenen Mandat verfasst wurde
     (verhindert **Mandatssubstitution**: Ein Agent mit einem laxen Mandat A kann kein
     B-geschlossenes-Mandat gegen Grant A auswerten lassen). Der Grant ist **niemals** das rohe
     Blob, das als selbstbehauptete Autorität behandelt wird.
   - **Evidenz** ist das **vollständige signierte Artefakt**: das offene SD-JWT, das
     geschlossene Key Binding JWT und die **tatsächlich vorgelegten Disclosures** — aufbewahrt
     (verschlüsselt, zugriffskontrolliert), damit ein Streitfall *die
     Signaturverifikationssequenz von AP2 wiederholen* kann, was ein Hash nicht kann. Diese
     Evidenz enthält PII (Beträge, Zahlungsempfänger); sie ist daher **verschlüsselte Evidenz
     nach dem Prinzip des notwendigen Minimums, nicht „niemals PII“** — die Minimaldatenregel
     gilt für *Autorität/Grant* und für Betriebsprotokolle, nicht für den versiegelten
     Streitfall-Datensatz.

2. **Signaturverifikation — Kette, mit gepinnten Algorithmen und getrennten Trust Roots.**
   Die SD-JWT-Kette und die Verbindung offen→geschlossen über das `cnf`-gebundene Key Binding
   JWT (PoP) verifizieren, bestätigen, dass das geschlossene Mandat die Claims des offenen
   Mandats unverändert erhält, und jedes Constraint auswerten (Korrekturen 2 und 3). Zwei
   Härtungsregeln, die die rohe Spezifikation nicht liefert:
   - **Algorithmus-Pinning.** Jeden Trust-Root-Schlüssel an seine erlaubte Algorithmenmenge
     binden und strikt dagegen verifizieren; **den vom Token angegebenen `alg` ignorieren**.
     `alg:none`, HS/ES-Verwechslung und Kurven-/Stärke-Downgrade ablehnen — AP2s
     Ed25519-Verbot ist eine einzelne enge Regel innerhalb einer header-gesteuerten
     Verhandlungsoberfläche, die der nicht vertrauenswürdige Agent treibt.
   - **Getrennte Trust Roots.** Der **User-Credential**-Root (OpenID4VP) verifiziert, dass der
     *Mensch* das offene Mandat *autorisiert hat*; die **Trusted-Agent-Provider**-Liste
     steuert ausschließlich, welche Agentenidentität den `cnf`-Schlüssel **halten/binden**
     darf. Sie attestieren unterschiedliche Fakten und sind **beide für ihre jeweils eigene
     Obliegenheit erforderlich** — niemals ein austauschbares ODER (eine
     Agent-Provider-Attestierung ersetzt nicht die Autorisierungssignatur des Benutzers).
     Deny-closed, wenn der erforderliche Root fehlt.

3. **Ablauf, Einmalverwendung und Widerruf (auf Olivares-gegatete Flows gescoped).** AP2 hat
   **keinen Widerruf**. Olivares schließt dies für **In-Path**-Deployments: (a) Der
   mandatsgebundene Grant ist **erstklassig widerrufbar** — sein Widerruf macht jede *künftige
   Olivares-Autorisierung* für dieses Mandat deny-by-default (Korrektur 1); er erreicht kein
   geschlossenes Mandat, das bereits an das Settlement freigegeben wurde (dieselbe Grenze wie
   bei AP2 — ehrlich ausgewiesen). (b) Ein geschlossenes Mandat mit hohem Wert verbraucht eine
   **Einmal-Genehmigung**, sodass eine Genehmigung nicht wiedereingespielt werden kann. (c)
   `exp`/`execution_date`/Wiederholung werden gegen die **vertrauenswürdige signierte
   DDIL-Uhr** durchgesetzt, und der SD-JWT-Adapter bezieht sein `now` aus derselben Uhr, sodass
   die beiden Schichten nicht auseinanderlaufen können.

4. **Replay / Double-Spend — Deduplizierung auf Verifier-Seite ist VERPFLICHTEND (in-path).**
   AP2 legt das Anti-Double-Spend-MUST auf den *Shopping-Agenten* (in seinem eigenen
   Threat-Model ein Angreifer) und macht die Prüfung des Verifiers nur zu einem MAY. Der
   Olivares-PDP verfolgt vorgelegte Nonces / `transaction_id`s geschlossener Mandate je
   offenem Mandat und weist überlappende/wiederholte Vorlagen zurück — für Autorisierungen,
   die über Olivares laufen (die In-Path-Vorbedingung).

5. **Was Olivares NICHT tut.** Keine Verwahrung von Geldern, keine Zahlungsausführung, keine
   Karten-/Token-Ausgabe, kein Auftreten als PSP/Netzwerk/Wallet. Olivares ist der **PDP**,
   der den agentischen Kauf gegen die Policy autorisiert, und die **Evidenzebene**, die
   Mandat/Receipt versiegelt. Das Settlement verbleibt bei Händler/PSP/Netzwerk.

### Konsequenzen

- **Gut:** Wiederverwendung von Cedar/Reserve-Ledger/Genehmigungen/Audit-Kette dort, wo die
  Semantik wirklich passt; AP2s Lücken werden zu durchgesetzten Garantien; versiegelte, nicht
  abstreitbare Evidenz; ehrliche, verifizierbare Positionierung.
- **Schlecht / Abwägungen:** Die Wiederverwendung ist **bedingt** — sie braucht ein
  Deny-by-default-Overlay für Zahlungs-Actions, einen fail-closed Translator,
  Full-Disclosure-Enforcement, einen Reservierungsschlüssel je Mandat und einen komplett neuen
  Wiederholungs-Limiter (nichts davon gratis); AP2 ist pre-1.0 (ein v0.3 wird ein Re-Mapping
  erzwingen, isoliert hinter dem Adapter und an `vct` gepinnt); das Aufbewahren signierter
  Evidenz mit PII schafft eine Verschlüsselungs-/Aufbewahrungspflicht.
- **Neutral / Follow-ups:** Agent-zu-Agent-Mandatsdelegation ist **außerhalb des AP2-Scopes** →
  außerhalb unseres; x402 (AP2-Erweiterung für Krypto-Rails) und ACP (OpenAI/Stripe) sind
  getrennt und werden verfolgt, hier aber nicht gebaut.

## Warum die Alternativen verworfen wurden

- **Option B (maßgeschneiderte Engine)** — abgelehnt: Sie dupliziert die
  Reserve-Ledger-/Genehmigungs-/Audit-Mechanik für ein pre-1.0-Protokoll; die Korrekturen oben
  zeigen, dass die Wiederverwendung tragfähig ist, sobald das Deny-by-default für
  Zahlungs-Actions und die fail-closed Übersetzung vorhanden sind.
- **Option C (nur beobachten)** — abgelehnt: Die ratifizierte Richtung ist, jetzt zu entwerfen
  und den Enterprise-Build früh zu beginnen, *ohne das öffentliche Release zu blockieren*. Nur
  zu beobachten würde das Differenzierungsmerkmal (governte agentische Ausgaben mit
  versiegelter Evidenz) verspielen, während sich der Standard bei FIDO konsolidiert. Dem
  Anliegen der ehrlichen Kennzeichnung wird entsprochen, indem jetzt der **Entwurf**
  ausgeliefert und der **Build** hinter verifiziertem Bedarf gegatet wird, nicht indem nichts
  getan wird.
