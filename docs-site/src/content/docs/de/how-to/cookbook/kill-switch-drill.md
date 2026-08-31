---
title: "Rezept: der Estate-Kill-Switch (und wie man ihn übt)"
description: >-
  Ein Aufruf stoppt jede governte Actuation im Estate — oder einen einzelnen
  Agent. Per Design schnell auszulösen; das Wieder-Aktivieren braucht zwei
  Menschen, und der Vorfall hinterlässt ein Evidence Pack. Übe ihn, bevor du
  ihn brauchst.
sidebar:
  order: 5
---

**Ziel:** wenn ein Agent in Maschinengeschwindigkeit aus dem Ruder läuft, ihn
— oder alles — *jetzt* stoppen, mit einem authentifizierten Aufruf, und den
Stopp später unter Dual Control wieder aufheben, mit dem gesamten Vorfall im
Protokoll.

Die Asymmetrie ist Absicht: **Auslösen ist schnell** (Admin-Tier, kein
Genehmigungs-Gate — ein Notstopp darf niemals in einer Queue warten),
**Wieder-Aktivieren ist langsam** (zwei verschiedene Menschen, und der Vorfall
hinterlässt ein Evidenzpaket für die Nachprüfung). Es gibt bewusst kein Break-Glass um den
Stopp herum: gestoppt *ist* der sichere Zustand.

## Auslösen

```bash
# Das gesamte Estate stoppen:
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"scope_kind":"estate","reason":"runaway agent incident #1234"}'

# Oder einen einzelnen Agent stoppen (per UUID oder External-ID):
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"scope_kind":"agent","scope_ref":"agent:billing-reconciler","reason":"…"}'
```

Was sofort und fail-closed stoppt: die governten **Actuation**-Flächen —
`claude.tool.use`, `mcp.tool.call`, `deploy.apply`, `deploy.retire`,
`orchestration.schedule.fire`, `voice.session.open`. Ausstehende
Actuation-Genehmigungen im Geltungsbereich werden **in derselben Transaktion
storniert**, sodass nichts Genehmigtes-aber-noch-nicht-Ausgeführtes nach dem
Stopp durchrutscht.

Was bewusst *nicht* stoppt: Observation und die Governance selbst (Findings,
Identitäts-Lebenszyklus, Compliance) — du kannst auch im gestoppten Zustand
weiterhin sehen und governen. Das erneute Auslösen eines bereits gestoppten
Geltungsbereichs liefert `409` (es ist idempotent auf dem Scope, kein Stack).

```bash
# Live-Posture — ist gerade irgendetwas gestoppt?
curl -ks "$BASE/v1/m/governance/killswitch/state" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Guardian-Regeln können denselben Stopp automatisch auslösen (`stop_agent` /
`stop_estate` Aktionen), wenn eine Containment-Regel feuert — der Auto-Pfad und
der menschliche Pfad sind dasselbe Gate, und ein Auto-Stopp emittiert ein
CRITICAL-Finding.

## Wieder-Aktivieren (Dual Control)

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/reenable" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"reason":"root cause fixed: …"}'
```

Dies **öffnet eine Genehmigung**, hebt den Stopp niemals direkt auf. Die Aktion
ist als CRITICAL vorklassifiziert: **zwei verschiedene menschliche Approver**,
starke (AAL3-)Authentifizierung pro Entscheidung — und die
Zwei-Menschen-Untergrenze ist strukturell, in der Transaktion durchgesetzt,
selbst wenn eine Genehmigungs-Policy versucht, den Tier herabzustufen. Der
Anfragende kann kein Entscheidender sein; eine abgelehnte oder abgelaufene
Anfrage öffnet ein frisches Quorum.

Nach dem Wieder-Aktivieren schließt ein **Post-Review** durch noch einen
weiteren Menschen (verschieden vom Auslösenden, vom Anfragenden *und* von den
Wieder-Aktivierenden) den Vorfall ab — bis er festgehalten ist, kann derselbe
Geltungsbereich nicht erneut ohne Review gestoppt-und-wieder-aktiviert werden:

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/review" … 
curl -ks "$BASE/v1/m/governance/killswitch/$STOP_ID/evidence"   # das Evidence Pack
```

Der Evidence-Endpunkt liefert das Pack des Vorfalls — den Stopp, die
stornierten Genehmigungen, die Entscheidungen und die Spur — bereit für den
Auditor.

## Die Konsole

**Kill switch** im Bereich Management ist die Ein-Klick-Variante desselben
Gates, mit dem Live-Zustand und dem Wieder-Aktivierungs-Flow:

<img class="light:sl-hidden" src="/console/killswitch-dark.png" alt="Die Kill-switch-Konsolenansicht: Estate-Zustand und Verlauf pro Stopp." />
<img class="dark:sl-hidden" src="/console/killswitch-light.png" alt="Die Kill-switch-Konsolenansicht: Estate-Zustand und Verlauf pro Stopp." />

## Übe ihn

Ein Kill-Switch, den du nie gezogen hast, ist eine Hypothese. Quartalsweise, in
einem Wartungsfenster:

1. Löse einen **agent-scoped** Stopp auf einem Agent mit geringem Risiko aus;
   verifiziere, dass seine Tool-Calls verweigern und das Finding feuert.
2. Gehe das Wieder-Aktivieren durch: zwei Approver, Post-Review, Evidence Pack
   gezogen und archiviert.
3. Stoppe die Zeit der Schleife von Anfang bis Ende — diese Zahl ist deine
   reale Containment-Latenz, und die Übung hinterlässt eine vollständige
   Ledger-Spur als Beleg dafür.
