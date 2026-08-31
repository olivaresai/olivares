> Traduction automatique. La version anglaise fait foi.

# ADR-0022: Scoping des sources par axe de subject (session / agent / utilisateur / groupe d'utilisateurs / rôle), avec effect au niveau de la ligne et posture d'enforcement versionnée à double contrôle

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## Contexte et énoncé du problème

Le binding de source (`modules/sourcescope`) lie une source connectée — un serveur MCP, un
modèle, un fournisseur, une base de connaissances, une source de données — à exactement un
des trois arbres de scope de **containment** : `workspace`, `agent_group` ou `folder`
(`schema.go:52-62`, `binding.go:33`). Il répond à la question : « un acteur **dans** ce
scope peut atteindre cette source ».

La vision du produit exige quatre axes supplémentaires que le modèle de containment ne peut
pas exprimer de manière ergonomique :

- **« cette SESSION voit la source X »** — une seule session en cours d'exécution.
- **« cet UTILISATEUR / ce groupe d'utilisateurs accède aux sources Y »** — une personne
  nommée et un groupe de personnes du répertoire.
- **« cet AGENT précis (et non son groupe) ne voit que Z »** — un agent, et non le groupe
  d'agents auquel il appartient.

Aujourd'hui, ces axes sont seulement *approximés* par la rédaction d'un grant Cedar brut —
aucune ergonomie de binding, aucune ligne listable/auditable, aucune projection dans la
access map et, pour la question inverse « quelles sources le subject S peut-il atteindre ? »,
le problème non résolu de requête inverse (`accessmap.go:44`). Pendant ce temps, la
gouvernance des **modèles** porte déjà un riche modèle de SUBJECT —
`subject_kind ∈ {user, role, agent_group}` avec des lignes allow/forbid et une algèbre
`forbid-overrides-allow` (`modelgovernance.go:98-100`, `modelaccessgate.go:204`). Il existe
une asymétrie de gouvernance : **les modèles sont gouvernés finement par subject ; les
sources sont gouvernées étroitement par containment.** Cette décision la corrige.

Une exigence de second ordre provient de l'analyse des acteurs établis (vérifiée par rapport
à la documentation des fournisseurs le 2026-07-07) : AWS Q Business transforme le
*relâchement* d'une ACL en une opération IAM dédiée, unidirectionnelle et auditée
(`qbusiness:DisableAclOnDataSource`) ; la posture ACL du data store de Google est
**immuable après sa création**. Notre différenciateur réside dans une posture **mutable,
versionnée et auditée** — mais son assouplissement doit être une opération **privilégiée, à
double contrôle et auditée**, jamais un simple toggle silencieux. Aucun acteur établi
n'exprime le scoping de sources par agent ou par session — cet espace libre est vérifié, ce
n'est pas une hypothèse.

## Facteurs de décision

- **Cohérence avec model-access.** Le même vocabulaire de subjects et la même algèbre
  `forbid-overrides-allow`, afin que les opérateurs raisonnent sur « qui peut atteindre une
  source » exactement comme sur « qui peut utiliser un modèle ».
- **Coût du chemin chaud.** Le resolver s'exécute sur le chemin EXECUTE de models
  (`ScopeGate`) et sur le chemin de retrieval de knowledge (`RetrievalScopeGate`). Les axes
  d'identité ne doivent pas ajouter un aller-retour de politique par résolution.
- **Auditabilité et requête inverse.** « Lister chaque source scopée à la session S / à
  l'utilisateur U / au groupe G » doit être une seule requête indexée, et non un parcours
  inverse de Cedar (la requête inverse n'est pas résolue).
- **UI.** Une seule forme de binding que la console (un suivi) puisse afficher et rédiger.
- **Rétrocompatibilité et sécurité.** Un déploiement sans nouveaux bindings décide
  exactement comme avant ; les axes d'identité doivent se lier au **principal authentifié**,
  et non à une chaîne déclarée par l'appelant, partout où cela est possible ; le plan de
  contrôle ne doit jamais gagner un second moteur d'autorisation, afin de conserver une
  petite surface d'attaque.

## Résultat de la décision

**Enrichir en place le binding de source existant (candidat A1) : ajouter des arbres de
scope par subject et un `effect` au niveau de la ligne, donnant à `sourcescope` une algèbre
allow/forbid scopée par subject qui reflète model-access sur sa propre table — tout en
laissant le modèle de containment et l'override Cedar inter-scope exactement en l'état.**
Ne pas rédiger de Cedar brut pour les nouveaux axes (candidat B), ni mettre en place un plan
de décision parallèle jumeau de model_access (candidat C). Un seul plan de contrôle, une
seule surface de requête, un seul endroit où réussir l'autorisation.

### 1. Cinq nouveaux arbres de scope par subject, uniformes avec les arbres de containment existants

`scope_tree` passe de `{workspace, agent_group, folder}` à l'ensemble suivant :

| tree | `scope_ref` | correspond lorsque… | source d'identité | falsifiable ? |
|---|---|---|---|---|
| `session` | `external_id` de la session | session active == ref | ref de l'appelant conscient de la session, renforcée par l'identité de l'agent | conditionné par la route (voir §4) |
| `agent` | `external_id` de l'agent | agent actif == ref | `principal.AgentIdentity` ∨ agent de la session ∨ ref de l'agent | conditionné par la route / authentifié |
| `user` | id de l'utilisateur | `principal.UserID` == ref | **principal authentifié** | non |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **principal authentifié** (fermeture imbriquée conditionnée par le groupe du répertoire) | non |
| `role` | nom du rôle du tenant | `principal.RoleIn(tenant)` == ref | **principal authentifié** | non |

`user_group` est le **groupe du répertoire** — comparé par **id de groupe** à
`principal.GroupsIn(tenant)`, déjà transporté sur le principal authentifié et qui intègre
déjà toute la fermeture des ancêtres imbriqués (`principal.go:67-77,151-164`) ; aucune
lecture de groupe par résolution n'est ajoutée. `UserGroup` n'a pas de slug
(`model/auth.go:122`), l'id est donc l'identifiant stable. `role` est ajouté pour une
**parité complète avec model-access** (Fran Olivares, 2026-07-07) : gouverner une source
par rôle du tenant est le levier grossier de « groupe d'utilisateurs » également exposé par
model-access.

Les trois axes d'identité individuelle (`session`, `agent`, `user`) sont des containments
dégénérés (égalité) ; `user_group` et `role` sont de véritables appartenances. Tous sont
évalués comme un **prédicat de scope** uniforme sur l'acteur — aucun nouveau moteur de
décision.

**La validation suit une dichotomie containment/subject (contrainte vérifiée).** Les
handlers d'écriture du module détiennent un `store.Scope` du tenant métier ; les subjects
d'authentification (`model.User`, `model.UserGroup`, rôles) résident dans `store.AuthScope`
(le tenant système) et ne sont **pas accessibles** depuis celui-ci (`core/store/store.go`
contre `auth.go:24-36`). Par conséquent :

- Les **arbres de containment** `workspace` / `agent_group` / `folder` **et** les arbres de
  subject résidant dans le store sont validés quant à leur existence lors du binding comme
  aujourd'hui (deny-closed, « aucun scope orphelin ») — mais, afin de conserver une règle
  uniforme et de pouvoir lier une source en amont d'une session éphémère, cette décision
  traite **les cinq arbres de subject selon leur forme uniquement** lors de la rédaction :
  un `scope_ref` non vide du bon type, sans lookup dans le store.
- La correction ne dépend pas de la validation d'existence : une ref de subject inconnue ne
  correspond simplement jamais à l'acteur authentifié lors de la résolution ⇒ deny-closed
  — **exactement le modèle de model-access** (`modelaccessgate.go` valide uniquement la
  *forme* du subject ; `validateGrantRefs` vérifie seulement le TARGET résidant dans le
  store). La prévention des fautes de frappe relève de la console (rédaction depuis un
  sélecteur de répertoire/d'agents), pas de la couche de binding. Les arbres de containment
  conservent leur validation d'existence actuelle inchangée.

### 2. Un `effect` au niveau de la ligne (allow | forbid), avec un **forbid-overrides-allow** absolu

Chaque binding porte `effect ∈ {allow (default; empty stored value = allow), forbid}` (la
même convention que `normalizeEffect` de model-access). L'algèbre du resolver devient, pour
un `(actor, source)` :

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**Changement de comportement documenté (comme l'ADR-0019 a documenté le sien).**
Aujourd'hui, le forbid du binding de source s'applique *par binding* : un
`EffectForbid` inter-scope sur un binding est ignoré par `continue`, et un binding
*différent* peut encore autoriser (`resolver.go:243-248`). Cette décision rend **tous** les
forbids **absolus** (`effect=forbid` au niveau de la ligne comme `EffectForbid` inter-scope) :
tout forbid correspondant refuse la source, primant sur le containment, le grant
inter-scope **et** le RBAC du tenant — l'algèbre exacte de model-access
(`modelaccessgate.go:204`) et du noyau Cedar (`EffectForbid` « PRIME sur tout »,
`authorizer.go:101`). Le sens est strictement plus sûr (un forbid peut uniquement refuser)
et aucun test existant de forbid à binding unique ne régresse ; le changement est visible
uniquement dans le cas jusque-là non spécifié de plusieurs bindings.

**Déclencheur du confinement.** Une source est *confined* si et seulement si elle possède
au moins un binding **allow** activé. Tous les bindings préexistants sont des allows, ce qui
est identique au comportement actuel « liée ⇔ possède des bindings ». Une source portant
**uniquement des forbids** reste globale, sauf pour les subjects nommés par ces forbids — la
posture de model-access « restreindre certains subjects », désormais disponible pour les
sources. Le gate d'affectation des connecteurs se base sur « aucun binding allow »
(auparavant « aucun binding »), de sorte qu'une source avec seulement des forbids respecte
toujours les affectations de connecteurs.

### 3. Priorité : forbid absolu ; credential selon l'allow le plus spécifique

Forbid est absolu (§2) ; la priorité ne décide donc jamais entre autoriser et refuser — elle
détermine **quel credential** reçoit un acteur autorisé lorsque plusieurs bindings allow
correspondent. L'ordre, du plus spécifique au moins spécifique, est :

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

D'abord l'identité individuelle, puis le groupe du répertoire, le rôle RBAC, le groupe de
l'agent actif et enfin le containment des ressources. Cet ordre total rend déterministe la
sélection du credential (en remplacement du tri lexical de `loadEnabledBindings`) et
constitue la priorité documentée `session > agent > group > workspace`, affinée pour les
cinq axes.

### 4. La disponibilité des axes dépend du point d'enforcement — et l'énonce honnêtement

Le resolver possède deux entrypoints qui portent des contextes d'acteur différents :

| axe | `ResolveForSession` (models `ScopeGate`, runtime) | `ResolveForAgent` (knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ ref de la session active | ❌ aucune session dans le contexte → ne correspond jamais |
| agent | ✅ agent de la session (override par identité de l'agent) | ✅ ref de l'agent (override par identité de l'agent) |
| user / user_group / role | ✅ principal authentifié | ✅ principal authentifié |
| workspace / agent_group / folder | ✅ (existant) | ✅ (existant) |

Un binding `session` sur une base de connaissances n'est **pas** appliqué sur le chemin de
retrieval réservé à l'agent, car aucune session n'y existe — il n'est pas silencieusement
« autorisé », il ne relève simplement pas du scope de cet acteur ; les autres
bindings/axes de la même source continuent de s'appliquer. Cette asymétrie est documentée
dans le contrat, et non dissimulée. Les axes `session`/`agent` restent conditionnés par la
route ; les références influencées par l'appelant sont renforcées par la vérification de
l'identité de l'agent (`principal.AgentIdentity` prime sur une ref déclarée par l'appelant).
`user`/`user_group`/`role` se lient au **principal authentifié non falsifiable** et sont donc
les axes les plus robustes.

### 5. La posture d'enforcement est mutable, versionnée et auditée — son assouplissement est soumis à un double contrôle

La *posture* d'une source est l'ensemble de ses bindings activés et de leurs effects. Selon
Fran Olivares (2026-07-07, « robuste sans duplication ») : **`revision.go` et
`approvals.go` de governance sont internes au module et NON réutilisables depuis
`sourcescope`** (vérifié : helpers non exportés, entités propres, flux d'approbation REST).
Les forker dans `sourcescope` créerait une dette technique dupliquée ; les contrôles de
posture sont donc **autonomes** et réutilisent l'unique primitive immuable partagée déjà
existante — le ledger d'audit :

- **Auditée et versionnée par la chaîne d'audit.** Chaque mutation de posture enregistre le
  **delta** de posture dans le ledger d'audit append-only et chaîné par hash (ADR-0009) —
  `sourcescope.binding.*` pour create/update/delete (`auditBinding` étant étendu avec
  l'`effect`) et `sourcescope.posture.{propose,approve,reject}` pour le cycle de vie à double
  contrôle. Le ledger EST l'historique des versions immuable et séquencé ; une *table de
  révisions numérotées avec rollback* dédiée n'est volontairement PAS ajoutée (elle
  dupliquerait `governance/revision.go`). Les lignes de **posture-request** en attente/
  décidées forment l'enregistrement de première classe et interrogeable de chaque
  *assouplissement* (qui l'a proposé, qui l'a approuvé).
- **Double contrôle dans le sens de l'assouplissement uniquement, autonome.** Une mutation
  susceptible d'**élargir** l'accès à une source est un *assouplissement* : elle n'est PAS
  appliquée par l'acteur — elle est enregistrée comme `sourcescope_posture_request` en
  attente et appliquée uniquement lorsqu'un **SECOND principal DISTINCT** l'approuve (la
  vérification `proposer != approver` garantit l'intégrité à deux personnes), l'approbateur
  possédant la permission de niveau admin `sourcescope:posture:admin` (séparation des tâches
  par rapport au proposant de niveau editor).

  > **Amendement de statut, 2026-08-07.** L'énumération ci-dessous est CORRIGÉE. Telle
  > qu'écrite à l'origine, elle listait *élargir un allow* et *déplacer un allow*, ne nommait
  > **aucune** opération de scope sur un `forbid`, et plaçait « resserrement vers un arbre plus
  > spécifique » parmi les écritures ordinaires d'un seul acteur **sans la qualifier par
  > effect**. Le code implémentait cela fidèlement : un `forbid` qui restait un `forbid` activé
  > et ne changeait que la population couverte s'appliquait sur-le-champ, par un seul acteur —
  > alors que SUPPRIMER ce même forbid exigeait deux personnes. La barrière à deux personnes se
  > contournait en éditant au lieu de supprimer. Inverser les classificateurs en listes
  > blanches a révélé trois fuites supplémentaires de la même classe : un `allow` déplacé vers
  > un arbre « plus spécifique » ; le DERNIER `allow` activé transformé en `forbid` ; et la
  > création d'un `allow` sur une source DÉJÀ confined (la création n'était classifiée par rien
  > du tout). La règle générale en tête de ce point n'a
  > jamais changé et c'est elle qui autorise la correction : l'énumération a toujours été plus
  > étroite que la règle qu'elle prétendait préciser.

  **Les classificateurs sont des LISTES BLANCHES.** Ils énumèrent les écritures qui ne peuvent
  démontrablement pas élargir l'accès et traitent **tout le reste — y compris toute forme
  qu'ils ne reconnaissent pas — comme un assouplissement**. Une liste noire de formes
  assouplissantes fuit par construction, et celle-ci a fui à quatre endroits. Trois étaient
  des modifications d'un binding existant — un `forbid` qui resserre son scope, un `allow`
  déplacé vers un arbre « plus spécifique », et le DERNIER `allow` activé transformé en
  `forbid` ; la quatrième était la création, que rien ne classifiait. Les deux premières
  viennent d'avoir lu une opération de scope avec la polarité d'un `allow`. La troisième vient
  d'avoir lu l'EFFECT de la ligne en oubliant le CONFINEMENT que cette même ligne portait : une
  source n'est confined que tant qu'elle a un `allow` activé, donc l'écriture qui se lit « cette
  ligne ne peut plus que refuser » est aussi celle qui rend la source globale.

  **Un `forbid` INVERSE LA POLARITÉ de toute opération de scope, et c'est le piège.** Pour un
  `allow`, un scope plus petit atteint moins d'acteurs : resserrement. Pour un `forbid`, il en
  PROTÈGE moins : tous ceux qu'il cesse de couvrir sont dé-refusés par cette seule écriture.

  **Deux scopes ne sont comparables que lorsqu'ils sont le MÊME scope.** `specificityRank`
  (`resolver.go`) **ordonne les arbres pour choisir un CREDENTIAL** parmi les bindings allow
  correspondants ; **ce n'est pas une relation de contenance** et cela ne doit jamais servir de
  telle. `role:admin` et `user_group:g1`, `workspace:eng` et `agent_group:core`, un dossier et
  son enfant sont des POPULATIONS différentes dont aucune ne contient l'autre — et un binding
  de dossier n'a aucune dimension de contenance (il repose sur le grant Cedar cross-scope). L'appartenance
  n'est pas figée non plus : un sur-ensemble prouvé en lisant les lignes aujourd'hui n'en est
  plus un demain. Le certificat de « cette écriture ne peut pas élargir » est donc
  l'**identité du scope et rien de plus faible**, et « je ne sais pas comparer ces deux
  scopes » se résout en *assouplissement* : un faux positif coûte une approbation de trop, un
  faux négatif est le contournement d'une barrière à deux personnes.

  **Assouplissements**, précisément (`classifyCreate`/`classifyUpdate`/`classifyDelete`) :
  supprimer ou désactiver un **forbid** activé ; passer de `forbid→allow` ; **tout changement
  de scope sur un forbid activé** (il dé-refuse une partie de sa population) ; **activer** un
  allow ; désactiver ou supprimer le **dernier** allow activé (la source n'est plus confined →
  globale) ; **tout changement de scope sur un allow activé** — plus large, plus étroit ou
  latéral, indifféremment ; **créer un allow sur une source DÉJÀ confined** (un grant pour une
  population qui ne pouvait pas l'atteindre) ; ainsi que l'opération unidirectionnelle dédiée
  **`POST /sources/disable-scoping`** (pendant de `qbusiness:DisableAclOnDataSource` d'AWS).

  Les mutations de **resserrement / neutres** sont des écritures ordinaires d'un seul acteur —
  auditées, mais non conditionnées : ajouter un **forbid** ; `allow→forbid` ; créer le
  **PREMIER** allow activé sur une source non confined (il place la source sous gouvernance —
  le plus grand resserrement du module, délibérément sans barrière pour que le geste sûr ne
  soit jamais le coûteux) ; créer une ligne **désactivée** ; activer un **forbid** en sommeil ;
  supprimer ou désactiver un allow **autre** que le dernier ; et modifier une note/un
  credential en laissant effect, enabled et scope intacts (le localisateur de credential
  choisit QUELLE référence reçoit un acteur déjà autorisé, jamais S'IL est autorisé). Une ligne
  désactivée avant et après n'impose rien : toute écriture dessus est neutre.

  Cette asymétrie correspond à celle d'AWS (l'assouplissement est l'opération privilégiée) et
  dépasse la posture immuable de Google : la nôtre est mutable *et* gouvernée. Endpoints :
  le create/update/delete d'assouplissement est PROPOSÉ via les `POST /bindings` et
  `PUT`/`DELETE /bindings/{id}` existants (réponse `202` avec la requête en attente) ;
  `POST /posture-requests/{id}/{approve,reject}` décide ; `GET /posture-requests` est la
  file du reviewer.

### 6. La access map projette les nouvelles origines (ADR-0003)

`publishBindingEdges` projette le côté permis de la carte RRW. `EdgeObservation` prend déjà
en charge `OriginKind ∈ {agent, session, identity}` (`sdk/model/observation.go:55`) ; chacun
des trois axes d'identité individuelle projette donc UNE edge : un binding `session` → une
edge d'origine `session` (un binding par session apparaît comme edge de **cette** session) ;
`agent` → une edge d'origine `agent` ; `user` → une edge d'origine `identity`. Les axes de
subject de GROUPE (`user_group`, `role`) devraient énumérer leurs MEMBRES pour projeter les
edges — mais les membres sont des entités d'auth-scope (groupes de répertoire, utilisateurs)
inaccessibles depuis le `store.Scope` du tenant du module. Ainsi, exactement comme la
projection inverse des grants d'un binding de folder (report de la requête inverse), ils
sont **REPORTÉS** : journaliser et ne rien projeter. Les bindings forbid ne projettent rien
(un forbid n'est pas une edge autorisée). L'enforcement reste toujours la décision live du
resolver face au principal live ; la carte est une observabilité best-effort de la dérive,
et une edge reportée/absente ne l'affaiblit jamais.

## Conséquences

- **Avantages :** les quatre axes de la vision (cinq avec `role`) sont exprimables,
  appliqués deny-closed sur les deux PEP réels et visibles dans la résolution de scope et la
  access map ; une forme de binding auditable/listable pour la console ; les axes
  d'identité se lient au principal authentifié (non falsifiable) ; aucun second moteur
  d'autorisation (petite surface d'attaque) ; le chemin chaud paie une vérification
  d'appartenance peu coûteuse et **zéro** nouvel aller-retour de politique pour les axes
  d'identité ; une posture mutable mais gouvernée, différenciateur vérifié face à AWS
  (unidirectionnel) et Google (immuable).
- **Inconvénients / compromis :** `scope_tree` porte désormais à la fois une sémantique de
  « scope de containment » et d'« identité de subject » (atténuation : le contrat encadre
  les deux comme un *prédicat de scope* uniforme) ; la mécanique de posture/double contrôle
  ajoute une surface réelle qu'un déploiement minimal n'exerce pas avant de rédiger un
  assouplissement ; rendre forbid absolu est un changement de comportement documenté (sens
  sûr).
- **Neutre :** `role` recoupe conceptuellement le bypass d'isolation souple du RBAC du tenant
  existant (`rbacAllows`) — ils se composent (un binding `role` est un scope positif ; le
  bypass RBAC est la règle de visibilité de l'opérateur du tenant), et un forbid prime sur
  **les deux**.

## Pourquoi les alternatives ont été rejetées

- **(B) Une API de haut niveau qui génère des politiques Cedar pour les nouveaux axes.**
  Rejetée : (1) elle serait le *seul* plan à rédiger du Cedar brut, alors que model-access —
  la cible de cohérence — ne génère **pas** de Cedar ; il décide sur ses propres lignes
  (`modelaccessgate.go:11-14`). (2) Elle paie un aller-retour Cedar par résolution sur le
  chemin chaud. (3) La question inverse requise par la console (« quelles sources le
  subject S peut-il atteindre ? ») est la requête inverse Cedar non résolue ; l'UI et la
  access map seraient donc bloquées ou approximatives. (4) Auditer « qui a scopé quoi »
  oblige à lire le texte de la politique plutôt que des lignes.
- **(C) Une table parallèle jumelle de model_access pour les grants source-subject,
  composée avec le binding de containment existant.** Rejetée comme sur-ingénierie qui
  *réduit* la robustesse : deux plans de décision doivent être composés à chaque PEP et
  maintenus cohérents — source classique de dérive de sécurité (l'un mis à jour, l'autre
  non ; priorité inter-plan ambiguë). Le « plus complet/enterprise » s'obtient par la
  **profondeur sur un seul plan** (tous les axes + effect + posture versionnée à double
  contrôle + matrice de tests complète), et non en dupliquant la plomberie. Un plan de
  contrôle unique doté d'une algèbre uniforme est plus facile à auditer (« tout ce qui
  gouverne la source X » = une requête) et à prouver correct.
- **Étendre le vocabulaire scopeSpec des rôles personnalisés plutôt qu'un enum local.**
  Rejetée : le `scope_tree` de `sourcescope` est une constante locale au module qui ne fait
  que *refléter* le catalogue des rôles personnalisés (`schema.go:49`) ; élargir un
  catalogue partagé ferait fuir les axes de sources dans les cibles permises aux rôles
  personnalisés. Les nouveaux arbres restent locaux à `sourcescope`.
