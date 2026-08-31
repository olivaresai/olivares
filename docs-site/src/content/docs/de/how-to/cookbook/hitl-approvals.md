---
title: "Rezept: Human-in-the-Loop-Genehmigungen"
description: >-
  Destruktive Aktionen hinter governten Genehmigungen gaten: eine Anfrage öffnen,
  die an den exakten Plan gebunden ist, autorisierte Menschen mit serverseitig
  durchgesetzter Funktionstrennung und Ablauffrist entscheiden lassen und die
  Entscheidung im Ledger festhalten.
sidebar:
  order: 3
---

**Ziel:** „ein Deployment-Apply (oder ein Orchestrierungs-Fire oder das Öffnen
einer Voice-Session) findet nicht statt, bis ein Mensch, der *nicht* der
Anfragende ist, es genehmigt — und die Entscheidung ist ein festgehaltener
Fakt."

Die Genehmigungs-Engine ist im Standard-Binary aktiv; das
[Governance-Modell](/de/how-to/govern-and-approve/#die-human-in-the-loop-posture)
erläutert die Haltung. Dieses Rezept ist die operative Verdrahtung.

## 1. Das Genehmigungs-Gate verdrahten

Modulaktionen, die Infrastruktur mutieren würden, laufen durch die
Human-in-the-Loop-Brücke. Sie wird per Konfiguration aktiviert — ohne sie
bleiben diese Aktionen deny-closed:

```bash
OLIVARES_APPROVAL_BRIDGE_CONFIG=/etc/olivares/approval-bridge.json
```

Betreibe die Komponente, die Genehmigungen *öffnet*, als **eigenes
Service-Konto, das niemals im Approver-Pool ist**. Die Funktionstrennung wird
engine-seitig durchgesetzt (der Öffnende kann seine eigene Anfrage nicht
entscheiden, und ein System-Token kann überhaupt nicht genehmigen) — ist das
Konto des Öffnenden zugleich ein Approver, hast du einen
Liveness-Deadlock gebaut, keine Kontrolle.

## 2. Eine Anfrage öffnen

```bash
curl -ks -X POST "$BASE/v1/m/governance/approvals" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "subject_kind": "deployment",
    "subject_ref": "deploy:payments-api",
    "action": "deploy.apply",
    "reason": "rollout v2.4.1",
    "expires_in_seconds": 3600
  }'
```

Die Anfrage öffnet **deny-closed und zeitlich begrenzt**, gebunden an den
exakten Plan, den sie abdeckt. Wenn eine aktivierte Genehmigungs-*Policy*
`(action, subject_kind)` matcht, ist deren `required_approvals` maßgeblich —
ein Anfragender kann die Latte nicht von der Anfrageseite aus senken.

## 3. Entscheiden

```bash
# Die Queue (nach status / action filtern):
curl -ks "$BASE/v1/m/governance/approvals?status=pending" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Die Entscheidung (approval-admin-Berechtigung):
curl -ks -X POST "$BASE/v1/m/governance/approvals/$ID/decisions" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"decision":"approve","note":"reviewed the plan hash"}'
```

Was die Engine serverseitig durchsetzt — nichts davon ist Client-Konvention:

- **Funktionstrennung:** der Entscheidende wird über die stabile User-ID
  geschlüsselt; der Anfragende kann nicht entscheiden, und derselbe Mensch kann
  nicht zweimal entscheiden (ein Unique-Index, keine UI-Regel).
- **Ablauf:** eine abgelaufene Anfrage kann niemals eine bindende Entscheidung
  erhalten, selbst bevor der Sweeper den Zustand materialisiert.
- **Risk-Tier-Untergrenze:** als CRITICAL vorklassifizierte Aktionen (die
  Kill-Switch-Familie, Credential-Finalisierung und Verwandtes) erfordern
  **mindestens zwei verschiedene menschliche Approver mit starker
  (AAL3-)Authentifizierung pro Entscheidung** — und die Untergrenze ist
  strukturell: eine Genehmigungs-Policy, die den Tier herabzustufen versucht,
  wird am Entscheidungspunkt wieder auf die Untergrenze gehoben.

## 4. Der Eintrag

Jede Entscheidung wird in derselben Transaktion mit dem realen Akteur an das
Audit-Ledger angehängt — `GET /v1/m/governance/approvals/{id}/decisions` ist
die unveränderliche Spur, und der [Pull-Export](/de/how-to/forward-audit-to-splunk/)
trägt sie in dein SIEM. Du kannst keine governte Änderung vornehmen, die das
Ledger stillschweigend vergisst.

## Hinweise

- `escalate_in_seconds` benachrichtigt das SoD-Team, wenn eine Anfrage
  unentschieden liegen bleibt — nutze es für produktionskritische Aktionen.
- Abbruch (`POST …/{id}/cancel`) ist für den Anfragenden oder einen Admin auf
  einer ausstehenden Anfrage gedacht; er wird ebenfalls festgehalten.
- Was noch reift, ist die reichhaltigere Review-**Konsole**; die oben genannten
  engine-seitigen Garantien sind live
  ([ehrlicher Umfang](/de/how-to/govern-and-approve/)).
