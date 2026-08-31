> Traduction automatique. La version anglaise fait foi.

# ADR-0019: Cedar comme moteur de grants positifs et scopés (et non comme simple overlay deny-only)

- **Status:** accepted
- **Date :** 2026-06-15
- **Décideurs :** Fran Olivares
- **Références :** ADR-0013 (PDP — Cedar + OPA)

## Contexte et énoncé du problème

L'ADR-0013 plaçait Cedar derrière la jointure `auth.PolicyEvaluator` en tant qu'**overlay deny-only** :
un `permit(principal, action, resource)` de base implicite était compilé en amont de la
politique de l'opérateur, de sorte qu'une décision Cedar ne pouvait jamais être qu'un `forbid` (une restriction).
L'autorisation était donc **plate au niveau du tenant** — le RBAC intégré accordait une
permission sur l'ensemble du tenant, et la politique ne pouvait que la restreindre. Il n'existait aucun moyen
d'exprimer un **grant positif et scopé** : « cet administrateur ne peut gérer les agents que dans le workspace
X », « les viewers ne peuvent lire les ressources que sous le dossier Y », « ce rôle peut écrire dans le
groupe d'agents `payments` ». Le plan de scoping (workspace → groupe d'agents → agent →
ressource/dossier) modélisait l'arbre, mais rien n'*appliquait* de grants le long de celui-ci ; la
access map se contentait d'*observer* (`AccessEdge.Permitted` = « non connu comme permis »).

## Facteurs de décision

- Exprimer des grants positifs scopés à l'arbre (workspace, groupe d'agents, sous-arbre de
  ressources) et à des conditions (modèle, sensibilité, AAL) — appliqués sur le chemin réel.
- Conserver les garanties de deny-overlay et de deny-by-default établies par l'ADR-0013 (forbid
  prime toujours ; un grant manquant refuse toujours).
- Ne pas réimplémenter à la main la résolution de hiérarchie/appartenance — utiliser le moteur
  formellement vérifié qui la modélise nativement.
- Rétrocompatibilité : un déploiement sans grants rédigés doit décider exactement comme avant, et
  ne rien payer sur le chemin chaud.

## Résultat de la décision

**Élever le moteur Cedar embarqué d'un overlay deny-only à un moteur de grants à trois valeurs,
conscient du scope, derrière une NOUVELLE jointure `auth.ScopedAuthorizer` placée à côté (et non à l'intérieur)
du deny-overlay.**

1. **Décision à trois valeurs, sans hack de base-permit.** L'`Authorize` de cedar-go est
   deny-by-default et forbid-prime-sur-permit, et son `Diagnostic.Reasons` nomme les
   politiques déterminantes. Cela permet de retrouver, à partir d'une seule évaluation, l'effet
   dont l'Authorizer a besoin : `Allow` → **Grant** ; `Deny` avec raisons → **Forbid** ; `Deny` sans
   raisons → **Abstain** (deny-by-default). Le permit de base de l'ADR-0013 est supprimé — un
   `permit` accorde désormais réellement, un `forbid` restreint toujours, et une politique vide/non
   pertinente s'abstient afin que la décision RBAC tienne (l'invariant de rétrocompatibilité).

2. **L'algèbre de l'Authorizer devient** `Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`.
   Le moteur scopé s'exécute en premier (un forbid court-circuite, primant sur le RBAC et tout
   grant) ; la base est le grant RBAC à l'échelle du tenant OU un grant positif scopé ; le
   moteur ABAC natif + tout PDP externe la restreignent ensuite (défense en profondeur). Le deny-by-default
   et le fail-closed sont préservés ; un moteur scopé nul réduit l'Authorizer à son
   comportement ADR-0013.

3. **L'autorité de grant EST la politique Cedar rédigée par tenant** (la surface de rédaction
   de politiques), désormais compilée en mode grant. Le moteur résout la lignée de la
   ressource *réelle* de la requête (lue depuis le store par identifiant d'entité — non falsifiable) en un
   graphe d'entités Cedar dont les `Parents` encodent la containment, de sorte que le `in` transitif de Cedar
   effectue le parcours de hiérarchie. **Nous n'avons pas ajouté de store de lignes de grant structurées séparé** :
   la rédaction de grants structurée/console qui *se projette vers* Cedar relève de la couche de rédaction structurée ;
   le moteur scopé consomme la politique et l'applique.

4. **Les grants sont uniquement par tenant ; le Cedar d'environnement global et OPA restent deny-only.** Un
   *forbid* global trop large ne fait que refuser (sûr) ; un *permit* global trop large accorderait
   un accès inter-tenant (dangereux). Cette asymétrie est décisive : les grants positifs vivent dans la
   politique rédigée par tenant (que le moteur indexe par tenant), tandis que le Cedar
   d'environnement à l'échelle du déploiement (`OLIVARES_PDP_*`) et OPA demeurent des overlays restrict-only.

### Conséquences

- **Avantages :** autorisation scopée de niveau entreprise (workspace/groupe d'agents/dossier/modèle/
  sensibilité/AAL) appliquée au point d'étranglement REST + gRPC ; le moteur vérifié résout la
  hiérarchie/appartenance ; rétrocompatibilité et deny-by-default intacts ; le chemin chaud ne paie rien
  tant qu'un tenant n'opte pas pour les grants (le moteur s'abstient avant toute lecture du store).
- **Inconvénients / compromis :** un tenant ayant activé les grants paie une petite lecture de store contrôlée pour résoudre
  le scope d'une entité sur les requêtes au niveau entité (un cache de scope par tenant est le suivi
  documenté) ; les conditions d'arbre de scope ne se résolvent que contre la hiérarchie vivante dans le
  moteur activé, pas dans le dry-run de rédaction.
- **Changement de comportement (documenté) :** une règle `permit` d'opérateur que l'overlay ADR-0013
  neutralisait silencieusement ACCORDE désormais. Les politiques rédigées en forbid-only ne sont pas affectées.

## Pourquoi les alternatives ont été rejetées

- **Un schéma de lignes de grant structurées séparé dans le moteur scopé** — duplique le propre modèle de politique
  et la résolution de hiérarchie de Cedar ; le moteur vérifié exprime déjà les grants sous forme de
  politiques sur un graphe d'entités. La rédaction structurée relève de la couche de rédaction structurée, en se projetant vers le
  Cedar que le moteur consomme déjà.
- **Une politique Cedar générée par grant** — ne passe pas à l'échelle (croissance du policy-set, churn à
  chaque édition de grant) ; des politiques templatisées sur un graphe d'entités résolu permettent à une seule règle de couvrir un
  workspace/groupe/sous-arbre entier.
- **Rendre le Cedar d'environnement global capable de grant** — un garde-fou de tenant oublié sur un permit
  global accorde un accès inter-tenant. Les grants sont confinés à la politique par tenant.
