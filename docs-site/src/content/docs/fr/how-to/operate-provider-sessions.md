---
title: Exploiter une session de fournisseur
description: >-
  Enregistrez un profil de fournisseur pour un home Claude, Codex ou Grok déjà
  présent sur ce nœud, épinglez le binaire officiel du pilote, lancez une
  session gouvernée depuis la console ou la CLI, puis interrompez ou arrêtez le
  tour sans inventer d’exécuteur.
---

Cette page est le chemin **exploiter** des CLI officielles de fournisseur. Le
plan de contrôle lance un processus enfant dont il est propriétaire sous un
**profil de fournisseur**. Il n’installe pas le fournisseur, ne crée pas son
home et n’ouvre pas d’authentification interactive dans le navigateur.

Ce n’est pas le chemin connecteur/hook. Pour inventorier ou gouverner les
fichiers de configuration de Grok Build ou de Codex, utilisez
[Intégrer Grok Build](/how-to/integrations/grok/) ou
[Intégrer Codex](/how-to/integrations/codex/). Pour co-déployer Claude Code sur
le même hôte, utilisez
[Exécuter Claude Code avec Olivares](/how-to/run-claude-code-with-olivares/).

Source de ce comportement : section `[26.9.0]` de `CHANGELOG.md` (profils
de fournisseur, pilote Codex, pilote Grok), les références générées de
[console](/reference/console/) et de [configuration](/reference/configuration/),
`cmd/olivares/sessionruntime.go` et `web/src/features/agentops/types.ts`.

## Prérequis

Remplissez-les avant un lancement. Un élément manquant est un refus, pas un
repli.

1. Olivares AI est installé et le premier administrateur existe.
   Voir [Votre première heure](/fr/how-to/first-hour/) pour le jeton de configuration.
   L’authentification renforcée administrative (`admin_step_up`) est réglée sur
   `none` par défaut. Si un administrateur active `totp` ou `passkey`, satisfaites
   cette politique avant les opérations privilégiées
   (`core/api/middleware.go` `requireStepUp`).
2. La CLI officielle du fournisseur est déjà installée sur **ce nœud**. Le
   profil enregistre des homes qui existent déjà. Le serveur résout les chemins
   (absolus, liens symboliques résolus, répertoire existant) et ne crée, n’installe
   ni ne connecte (`web/src/features/agentops/types.ts` `CreateProfileRequest`).
3. Vous détenez `sessions:profile:read` pour ouvrir **Provider profiles**
   (`/provider-profiles`) et `sessions:profile:write` pour enregistrer.
   Lier une source exige `sessions:profile-binding:write` plus
   l’administration des sources. Lancer une exécution exige `sessions:run:write`.
   Permissions : [référence de la console](/reference/console/).
4. Le pilote doit disposer d’un exécutable sur **ce nœud**. Un binaire fixé
   explicitement est prioritaire ; sinon, le moteur le recherche au lancement.
   Les vérifications de préparation et de politique restent propres à chaque
   pilote.

| Pilote | Épinglez cette variable d’environnement | Lorsqu’elle n’est pas définie |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | Installation gérée vérifiée la plus récente, puis `claude` dans le `PATH` du moteur. |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Installation gérée vérifiée la plus récente, puis `codex` dans le `PATH` du moteur. |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Installation gérée vérifiée la plus récente, puis `grok` dans le `PATH` du moteur. |

La valeur fixe l’exécutable officiel que ce nœud peut utiliser. Sans valeur
fixée, installer un outil le rend disponible sans redémarrer le moteur. Sans
installation gérée ni exécutable dans `PATH`, le lancement est refusé.
`OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` suit le même ordre de résolution.

Pour les profils Claude avec `managed_injection` qui ne désignent aucun fournisseur,
`OLIVARES_SESSION_RUNTIME_WIF` ou `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` fournit
l’identifiant d’inférence de l’hôte. Un profil lié à un fournisseur utilise son
identifiant ; en cas d’échec, le lancement est refusé sans repli vers celui de l’hôte.
Un profil avec `provider_account_home` utilise la connexion autorisée de l’outil
et n’a besoin d’aucune des deux variables. Voir
[Ajouter un fournisseur](/fr/how-to/add-a-provider/).
Codex et Grok n’utilisent que le `auth_source` AUTHORIZED du profil :
`provider_account_home` ou `managed_injection`, sans repli entre eux et sans
valeur par défaut (`CHANGELOG.md` `[26.9.0]` ; `ProviderProfileDTO.auth_source`).

:::caution[Ce que cette page n’affirme pas]
`CHANGELOG.md` `[26.9.0]` indique que le comportement du pilote Grok est
prouvé contre un enfant ACP factice appartenant au produit, à travers le HTTP,
le runtime, le store et le groupe de processus réels. **La compatibilité avec
un compte Grok officiel authentifié est un travail distinct et n’est pas
affirmée ici.**
:::

## 1. Enregistrer un profil de fournisseur

Un profil de fournisseur est l’identité durable d’**une** instance de
fournisseur configurée sur **un** environnement d’exécution : pilote,
environnement propriétaire, et les `config_home` / `user_home` canoniques sous
lesquels l’enfant s’exécute. C’est une identité de configuration et de stockage,
pas un compte fournisseur authentifié (`CHANGELOG.md` `[26.9.0]` B1 ; texte de
console `agentops.profiles.subtitle`).

### Console

1. Ouvrez **Provider profiles** (`/provider-profiles`).
2. Sélectionnez **Register profile**.
3. Définissez le pilote (`claude`, `codex` ou `grok`), le `config_home`
   existant et le `user_home` existant. `environment_ref` peut être omis (ce
   nœud).
4. Enregistrez. La liste affiche `profile_ref`, le pilote, l’état et si le
   profil est activé dans cet environnement. Les chemins ne figurent **pas**
   dans la liste ordinaire.
5. Pour voir les homes stockés, utilisez **Reveal configuration**
   (`sessions:profile:admin`). Cette lecture est à la demande et est abandonnée
   lorsqu’elle est masquée. Elle ne transporte toujours aucune valeur
   d’identifiant.

Renommer, désactiver et activer conservent le même id et les mêmes homes.
**Retire** est irréversible, confirmé en tapant, et libère le home pour un
**nouvel** id.

### Ce que le moteur refuse

- Un profil dont le pilote n’est pas enregistré sur ce nœud reste visible et
  n’est pas lançable (`operable` n’est pas une garantie de lancement ;
  `GET …/launch-readiness` est le panneau des exigences).
- Un profil qui appartient à un autre environnement d’exécution est affiché
  comme étranger et n’est jamais lancé depuis ce nœud.
- Un lancement qui transmet `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` ou
  `GROK_HOME` est refusé. Ces noms appartiennent au profil
  (`agentops.create.profileEnvConflict`).

Il n’y a pas de capture de cet écran dans le jeu publié. Ne traitez pas une
image de l’onglet Connectors comme ce formulaire.

## 2. Lier une source (facultatif, pour l’attribution observée)

Une source peut être dédiée à un profil à la révision exacte du roster que ce
nœud a appliquée. La clé est l’id persistant de la ligne, jamais son nom
modifiable (`CHANGELOG.md` `[26.9.0]` B1 ; **Source bindings**
`/provider-bindings`).

1. Ouvrez **Source bindings** (`/provider-bindings`).
2. Liez l’`id` persistant de la source et l’`applied_revision` câblée par le
   réconciliateur de ce nœud. `GET /v1/console/sources` reporte les deux.
3. Révoquez la liaison pour arrêter l’attribution **nouvelle** au profil. Une
   enveloppe antérieure conserve sa liaison historique pendant la relecture.

Sans liaison, un enregistrement connu apparaît encore comme une ligne
d’observation `source`. Elle n’est pas fusionnée dans une exécution gérée. Voir
[Opération en direct et sessions](/reference/modules/ii-sessions/).

## 3. Lancer

Le point d’entrée productif de création exige `provider_profile_ref`. L’omettre
conserve l’ancien corps de requête, que cette API refuse
(`CHANGELOG.md` `[26.9.0]` B2 ; drapeau CLI `--provider-profile`).

Le dialogue de lancement propose les profils **actifs**. Le seul profil actif
est présélectionné ; s’il y en a plusieurs, aucun ne l’est et **Start** demande
d’en choisir un. Les sélections d’espace de travail et de modèle peuvent être
effacées ; le profil non (`CHANGELOG.md` `[26.9.0]` Fixed).

### Console

1. Ouvrez **Sessions** (`/sessions`). `/agentops` ouvre le même écran
   ([référence de la console](/reference/console/)).
2. Ouvrez **New session**, puis **Advanced launch options** (dans
   **More options** quand un outil est prêt).
3. Vérifiez **Provider profile** (`agentops.create.profile`) et saisissez le
   **First message** si vous en voulez un.
4. Définissez éventuellement l’espace de travail, le modèle, le modèle
   d’inférence et l’effort dans **Advanced options**. Modèle et effort restent
   des chaînes ouvertes appartenant au fournisseur sur les drapeaux officiels
   de l’agent pour Grok (`CHANGELOG.md` `[26.9.0]`).
5. Appuyez sur **Start**. Tant qu’il ne peut pas démarrer, la ligne sous le
   bouton dit ce qui manque. Seule la **référence** du profil est publiée. Le
   serveur résout les homes.

Le choix **Dossier** détermine où l’outil travaille. **Dossier temporaire pour
cette session** donne au run son propre répertoire ; choisir un dossier enregistré
utilise ce dossier. Ce choix est distinct du workspace qui autorise la session.
**Contexte → Détails** affiche workspace et dossier stockés ; le dossier temporaire
normal n’est pas un avertissement.

### Son propre worktree Git (facultatif)

Par défaut, une session travaille dans le dossier choisi. Si c’est la racine d’un
dépôt Git, vous pouvez demander un **nouveau worktree Git** : cochez **Travailler
dans un nouveau worktree Git** sous **Dossier**, ou exécutez
`olivares session start . --worktree`. La session travaille sur une nouvelle branche
(`olivares/` plus huit caractères de son ID) dans son propre worktree ; deux sessions
sur le même dépôt ne partagent donc pas leurs fichiers. Fusionnez cette branche
ordinaire du dépôt depuis votre checkout comme d’habitude.

- Les worktrees résident sous `<data directory>/session-worktrees`.
  `OLIVARES_SESSION_WORKTREE_DIR` les déplace et
  `OLIVARES_SESSION_WORKTREE_BRANCH_PREFIX` modifie le préfixe de branche.
- **Reprendre** continue dans le même worktree. Si seul son répertoire a été
  supprimé, le moteur le restaure sur la même branche.
- **Nettoyer**, **Supprimer** et `olivares session rm` suppriment worktree et branche
  lorsque celle-ci est fusionnée dans la branche courante du workspace, que le
  worktree est sur cette branche et n’a aucun fichier non commité. Sinon la libération
  est refusée (409) et indique la perte possible : travail non fusionné, HEAD
  détachée ou sur une autre branche, worktree inaccessible. Cochez **Supprimer aussi
  le worktree et sa branche** ou ajoutez `--discard-worktree` pour continuer ; pour
  un worktree inaccessible, cela libère la session et laisse le worktree en place.
  Les fichiers ignorés par Git, comme les sorties de build, sont supprimés avec lui.
- L’option est refusée (422) avant toute création pour un dossier hors de la racine
  du dépôt, un dépôt sans commit, un workspace ou ses dossiers en lecture seule,
  une isolation non native et une configuration Git avec filtre
  (`filter.<name>.clean`, `smudge`, `process`) ou inclusion d’un fichier. Les sessions
  lancées sans cette option restent inchangées.
- Le moteur exécute Git avec hooks du dépôt et moniteur du système de fichiers
  désactivés, sans votre configuration Git et avec limites de temps et de sortie.
  Un filtre Git LFS configuré là n’est donc pas appliqué : les fichiers LFS sont
  des pointeurs dans le worktree. Une session peut écrire dans le répertoire Git
  partagé du dépôt ; toutes les sessions le partagent. Un worktree isole les
  fichiers, pas les métadonnées Git.

### Ouvrir le travail nommé par un handoff

Un handoff peut préciser où se trouve son travail dans Git : son contenu accepte
`branch` et `sha` (identifiant complet de commit), facultatifs. Proposez-le via
l’API ou `olivares message handoff offer --context-file`, dont le JSON peut porter
les deux. Un handoff qui ne les indique pas reste inchangé.

À la lecture, le panneau affiche branche et commit sous forme de texte.
**Ouvrir dans un nouveau worktree de session** ouvre le lancement avec le worktree
sélectionné et son point de départ affiché. Le lancement attend le choix d’un
workspace dont le dépôt contient le commit. Effacer la sélection du dossier conserve cette
demande ; décochez explicitement le worktree pour lancer une session ordinaire.
En ligne de commande :

```sh
olivares session start . --worktree-from <commit or branch> --name review
```

`--worktree-from` implique `--worktree`. La session travaille sur sa propre nouvelle
branche à ce commit ; la branche de l’expéditeur et votre checkout ne bougent pas.
Si le commit manque dans le dépôt du workspace, le lancement est refusé (422)
avant toute création : récupérez-le d’abord avec fetch. Un commit que ne retient
aucune branche, étiquette ou branche distante est également refusé. Le volet
**Modifications de branche** liste alors ce que sa branche contient en plus du
commit courant du workspace et ouvre le texte d’un chemin à la base de fusion
à côté du texte au sommet de la branche. Il affiche uniquement le travail commité,
dans les sous-chemins autorisés et la posture DLP du workspace ; les modifications
non commitées figurent dans **Modifications**.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Ajoutez `--server`, `--tenant` et `--token-file` (ou le contexte client actif) comme
dans la [référence CLI](/reference/cli/). L’isolation est `native` dans cette
version ; `container` et `sandbox` renvoient HTTP 422 avant la création d’une
exécution. Choisissez `native` pour utiliser l’exécuteur intégré.

Résultat : une ressource d’exécution. La ligne live gérée est unique par portée
d’observation et id externe. Les lectures qui nomment une ligne utilisent
`live_ref`, pas l’id de session fournisseur nu que deux homes peuvent partager.

## 4. Interrompre ou arrêter

| Intention | Console | CLI | Résultat |
|---|---|---|---|
| Terminer le tour actif, conserver le processus et la conversation | commande interrupt sur la session live | `olivares agent session interrupt <run-ref>` | le tour se termine ; le processus reste utilisable pour le tour suivant (`CHANGELOG.md` `[26.9.0]`) |
| Terminer l’exécution | commande stop | `olivares agent session stop <run-ref>` | la ressource d’exécution ; le runtime continue de récolter l’enfant |

Les exécutions liées au travail envoient leur clôture de bail exacte. Les
résultats périmés ou incertains restent explicites. La reprise ne continue que
sur le même home prouvé.

Une interruption Grok utilise ACP `session/cancel`, une notification sans accusé
de réception. L’interruption résout les approbations en attente, annule, et
laisse le tour ouvert jusqu’au retour du résultat corrélé du prompt lui-même
(`CHANGELOG.md` `[26.9.0]`). Ne traitez pas une annulation silencieuse comme un
accusé de réception confirmé du fournisseur.

## Pages connexes

- [Votre première heure](/fr/how-to/first-hour/) — jeton de configuration, authentification renforcée administrative, source d’identifiants Claude.
- [Exécuter Claude Code avec Olivares](/how-to/run-claude-code-with-olivares/) — topologies de co-déploiement.
- [Intégrer Codex](/how-to/integrations/codex/) / [Intégrer Grok Build](/how-to/integrations/grok/) — connecteur et hook PEP.
- [API d’exécution de session](/reference/session-runtime-api/) — liste, attach, input, stop ; PTY Community et frontière d’édition.
- [Opération en direct et sessions](/reference/modules/ii-sessions/) — `live_ref` et attribution.
- [Configuration](/reference/configuration/) — variables d’épinglage du pilote.
- [Référence CLI](/reference/cli/) — `olivares agent session *` (générée à partir du binaire).
