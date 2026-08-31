> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0019: Cedar als positive, scoped Grant-Engine (kein Deny-only-Overlay)

- **Status:** accepted
- **Datum:** 2026-06-15
- **Entscheider:** Fran Olivares
- **Referenzen:** ADR-0013 (PDP — Cedar + OPA)

## Kontext und Problemstellung

ADR-0013 stellte Cedar hinter die Naht `auth.PolicyEvaluator` als **Deny-only-Overlay**:
ein impliziter Basis-`permit(principal, action, resource)` wurde vor der Policy des
Operators kompiliert, sodass eine Cedar-Entscheidung immer nur ein `forbid` (eine Einschränkung) sein konnte.
Die Autorisierung war daher **flach auf Mandantenebene** — das eingebaute RBAC gewährte eine
Berechtigung über den gesamten Mandanten hinweg, und die Policy konnte sie nur einschränken. Es gab keine Möglichkeit,
einen **positiven, scoped Grant** auszudrücken: "dieser Admin darf Agenten nur in Workspace
X verwalten", "Betrachter dürfen Ressourcen nur unter Ordner Y lesen", "diese Rolle darf in die
`payments`-Agentengruppe schreiben". Die Scoping-Ebene (Workspace → Agentengruppe → Agent →
Ressource/Ordner) modellierte den Baum, aber nichts *erzwang* Grants entlang dieses Baums; die
Access Map *beobachtete* lediglich (`AccessEdge.Permitted` = "nicht bekannt als erlaubt").

## Entscheidungstreiber

- Positive Grants ausdrücken, die auf den Baum (Workspace, Agentengruppe, Ressourcen-
  Teilbaum) und auf Bedingungen (Modell, Sensitivität, AAL) gescoped sind — erzwungen auf dem realen Pfad.
- Das Deny-Overlay und die Default-Deny-Garantien beibehalten, die ADR-0013 etabliert hat (forbid
  überschreibt weiterhin; ein fehlender Grant verweigert weiterhin).
- Die Hierarchie-/Mitgliedschaftsauflösung nicht von Hand neu implementieren — die formal
  verifizierte Engine nutzen, die dies nativ modelliert.
- Rückwärtskompatibilität: Ein Deployment ohne verfasste Grants muss exakt wie zuvor entscheiden und
  auf dem Hot Path nichts kosten.

## Entscheidungsergebnis

**Die eingebettete Cedar-Engine von einem Deny-only-Overlay zu einer dreiwertigen,
scope-bewussten Grant-Engine erheben, hinter einer NEUEN Naht `auth.ScopedAuthorizer` neben (nicht innerhalb)
des Deny-Overlays.**

1. **Dreiwertige Entscheidung, kein Base-Permit-Hack.** Cedar-gos `Authorize` ist
   deny-by-default und forbid-überschreibt-permit, und seine `Diagnostic.Reasons` benennen die
   ausschlaggebenden Policies. Das stellt den Effekt wieder her, den der Authorizer aus einer einzelnen
   Auswertung benötigt: `Allow` → **Grant**; `Deny` mit Gründen → **Forbid**; `Deny` ohne
   Gründe → **Abstain** (Default-Deny). Der Base-Permit von ADR-0013 wird entfernt — ein
   `permit` gewährt nun tatsächlich, ein `forbid` schränkt weiterhin ein, und eine leere/irrelevante
   Policy enthält sich, sodass die RBAC-Entscheidung Bestand hat (das Rückwärtskompatibilitäts-Invariant).

2. **Die Algebra des Authorizers wird** `Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`.
   Die scoped Engine läuft zuerst (ein forbid kurzschließt und überschreibt RBAC sowie jeden
   Grant); die Basis ist der mandantenweite RBAC-Grant ODER ein positiver scoped Grant; die
   native ABAC-Engine + jeder externe PDP schränken ihn dann ein (Defense in Depth). Default-Deny
   und Fail-closed bleiben erhalten; eine nil-scoped Engine reduziert den Authorizer auf sein
   ADR-0013-Verhalten.

3. **Die Grant-Autorität IST die je Mandant verfasste Cedar-Policy** (die Policy-Authoring-
   Oberfläche), nun im Grant-Modus kompiliert. Die Engine löst die Lineage der
   *echten* Ressource der Anfrage (aus dem Store per Entity-ID gelesen — nicht fälschbar) in einen
   Cedar-Entity-Graphen auf, dessen `Parents` die Containment kodieren, sodass Cedars transitives `in`
   den Hierarchie-Durchlauf erledigt. **Wir haben keinen separaten strukturierten Grant-Row-Store hinzugefügt**:
   strukturiertes/Konsolen-Grant-Authoring, das *auf* Cedar projiziert, ist Sache der strukturierten Authoring-Schicht;
   die scoped Engine konsumiert die Policy und durchsetzt sie.

4. **Grants gelten nur je Mandant; das globale env-Cedar und OPA bleiben Deny-only.** Ein
   zu breites globales *forbid* verweigert nur (sicher); ein zu breites globales *permit* würde
   mandantenübergreifend gewähren (unsicher). Diese Asymmetrie ist entscheidend: positive Grants leben in der
   je Mandant verfassten Policy (die die Engine nach Mandant indiziert), während das deployment-
   weite env-Cedar (`OLIVARES_PDP_*`) und OPA reine Restrict-only-Overlays bleiben.

### Konsequenzen

- **Gut:** Enterprise-taugliche scoped Autorisierung (Workspace/Agentengruppe/Ordner/Modell/
  Sensitivität/AAL), erzwungen am REST- + gRPC-Engpass; die verifizierte Engine löst
  Hierarchie/Mitgliedschaft auf; Rückwärtskompatibilität und Default-Deny bleiben intakt; der Hot Path kostet nichts,
  bis ein Mandant sich für Grants entscheidet (die Engine enthält sich vor jedem Store-Read).
- **Schlecht / Abwägungen:** Ein Grant-aktivierter Mandant zahlt einen kleinen, gegateten Store-Read, um
  bei Anfragen auf Entity-Ebene den Scope einer Entity aufzulösen (ein Scope-Cache je Mandant ist der dokumentierte
  Follow-up); Scope-Baum-Bedingungen lösen sich nur gegen die Live-Hierarchie in der
  aktivierten Engine auf, nicht im Authoring-Dry-Run.
- **Verhaltensänderung (dokumentiert):** Eine Operator-`permit`-Regel, die das ADR-0013-Overlay
  stillschweigend neutralisierte, GEWÄHRT nun. Forbid-only verfasste Policies sind nicht betroffen.

## Warum die Alternativen verworfen wurden

- **Ein separates strukturiertes Grant-Row-Schema in der scoped Engine** — dupliziert Cedars eigenes Policy-
  Modell und seine Hierarchieauflösung; die verifizierte Engine drückt Grants bereits als
  Policies über einen Entity-Graphen aus. Strukturiertes Authoring gehört in die strukturierte Authoring-Schicht und projiziert auf das
  Cedar, das die Engine ohnehin konsumiert.
- **Eine Cedar-Policy pro Grant generieren** — skaliert nicht (Wachstum des Policy-Sets, Churn bei
  jeder Grant-Änderung); templatisierte Policies über einen aufgelösten Entity-Graphen lassen eine Regel einen
  ganzen Workspace/eine ganze Gruppe/einen ganzen Teilbaum abdecken.
- **Das globale env-Cedar grant-fähig machen** — ein vergessener Mandanten-Guard bei einem globalen
  permit gewährt mandantenübergreifend. Grants sind auf die je Mandant geltende Policy beschränkt.
