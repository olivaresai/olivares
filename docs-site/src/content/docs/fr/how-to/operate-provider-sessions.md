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
   Voir [Votre première heure](/how-to/first-hour/) pour le jeton de
   configuration et la barrière passkey AAL3. La création de sources et les
   opérations de session privilégiées exigent AAL3
   (`core/api/middleware.go` `requireAAL3`).
2. La CLI officielle du fournisseur est déjà installée sur **ce nœud**. Le
   profil enregistre des homes qui existent déjà. Le serveur résout les chemins
   (absolus, liens symboliques résolus, répertoire existant) et ne crée, n’installe
   ni ne connecte (`web/src/features/agentops/types.ts` `CreateProfileRequest`).
3. Vous détenez `sessions:profile:read` pour ouvrir **Provider profiles**
   (`/provider-profiles`) et `sessions:profile:write` pour enregistrer.
   Lier une source exige `sessions:profile-binding:write` plus
   l’administration des sources. Lancer une exécution exige `sessions:run:write`.
   Permissions : [référence de la console](/reference/console/).
4. Le pilote correspondant est **enregistré sur ce nœud** en épinglant son
   binaire officiel. La préparation est par pilote. Il n’y a pas d’interrupteur
   partagé (`cmd/olivares/sessionruntime.go`).

| Pilote | Épinglez cette variable d’environnement | Lorsqu’elle n’est pas définie |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (défaut `claude`) | le chemin Claude utilise le nom d’exécutable par défaut |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Lorsqu’elle n’est pas définie, une installation gérée **enregistrée** (`olivares agent tool install --driver codex`) épingle l’exécutable du reçu. Sinon, les profils Codex restent observables et ne sont pas lançables. Le moteur ne cherche pas dans `PATH`. |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Lorsqu’elle n’est pas définie, une installation gérée **enregistrée** (`olivares agent tool install --driver grok`) épingle l’exécutable du reçu. Sinon, les profils Grok restent observables et ne sont pas lançables. Le moteur ne cherche pas dans `PATH`. |

La valeur est le binaire officiel que ce nœud peut exploiter. Le moteur ne
résout pas `codex` ni `grok` via `PATH`. La table de configuration générée
liste aussi `OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` avec la même règle
d’enregistrement ; cette page n’ajoute pas d’autres affirmations OpenCode.

Les lancements Claude exigent toujours une source d’identifiants d’inférence
(`OLIVARES_SESSION_RUNTIME_WIF` ou `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`).
Voir [Votre première heure §3](/how-to/first-hour/#3-lancer-une-session-claude-code-depuis-la-console).
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

Le dialogue de lancement propose les profils **actifs**. Aucun profil n’est
présélectionné. Les sélections d’espace de travail et de modèle peuvent être
effacées ; le profil non (`CHANGELOG.md` `[26.9.0]` Fixed).

### Console

1. Ouvrez **Operate sessions** (`/agentops`) ou **Observe sessions**
   (`/sessions`). Ils partagent un écran
   ([référence de la console](/reference/console/)).
2. Ouvrez le dialogue de lancement.
3. Sélectionnez **Provider profile** (`agentops.create.profile`). L’indication
   précise qu’un profil est obligatoire.
4. Définissez éventuellement l’espace de travail, le modèle, le modèle
   d’inférence et l’effort. Modèle et effort restent des chaînes ouvertes
   appartenant au fournisseur sur les drapeaux officiels de l’agent pour Grok
   (`CHANGELOG.md` `[26.9.0]`).
5. Envoyez **Request launch**. Seule la **référence** du profil est publiée. Le
   serveur résout les homes.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Ajoutez `--server`, `--tenant` et `--token` (ou le contexte client actif) comme
dans la [référence CLI](/reference/cli/). L’isolation est `native` dans cette
version ; `container` et `sandbox` sont acceptés par l’API et refusés par le
lanceur jusqu’à ce que ces exécuteurs soient livrés (aide CLI générée).

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

- [Votre première heure](/how-to/first-hour/) — jeton de configuration, AAL3, source d’identifiants Claude.
- [Exécuter Claude Code avec Olivares](/how-to/run-claude-code-with-olivares/) — topologies de co-déploiement.
- [Intégrer Codex](/how-to/integrations/codex/) / [Intégrer Grok Build](/how-to/integrations/grok/) — connecteur et hook PEP.
- [API d’exécution de session](/reference/session-runtime-api/) — liste, attach, input, stop ; PTY Community et frontière d’édition.
- [Opération en direct et sessions](/reference/modules/ii-sessions/) — `live_ref` et attribution.
- [Configuration](/reference/configuration/) — variables d’épinglage du pilote.
- [Référence CLI](/reference/cli/) — `olivares agent session *` (générée à partir du binaire).
