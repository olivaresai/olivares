---
title: API d’exécution de session (CLI officielles)
description: >-
  Surface HTTP Community qui liste, attache, alimente et arrête des processus
  Claude Code, Codex et Grok CLI possédés. Permissions, PTY, reprise et reconnexion.
---

Le plan de contrôle **lance la CLI du fournisseur**. Il ne remplace pas Claude
Code, Codex ni Grok Build. Les sessions et les terminaux sont un module de ce
produit, pas le produit.

Cette page documente les routes Community d’operate sous `/v1/m/sessions/runs`.
Elles existent déjà dans le [document OpenAPI bêta](/reference/api-beta/).
La 26.10 ajoute le contrat de driver, un runner PTY local et les parcours J01–J08
comme tests.

## Frontière d’édition

| Édition | Ce qu’elle fait | Ce qu’elle ne fait pas |
|---|---|---|
| **Community (cette page)** | Enfant local possédé : lancement, stdin/stdout/stderr, attach avec curseur, reprise de la conversation exacte, reconnexion d’un flux vivant, arrêt avec code de sortie observé. Les lignes de session et les preuves restent dans le module II. | Moteur Identity & Scale multi-panneaux, listener mTLS, entrée des panneaux du cockpit, chunk xterm |
| **Overlay Identity & Scale** | Moteur commercial session-cockpit (listener, panneaux, ledger). Routes sous `/v1/m/session-cockpit/` lorsque l’module est présent. | Ne remplace pas `/v1/m/sessions/runs` |

Une build Community répond à l’espace de noms de l’overlay par **absence**
(404). Elle ne monte pas un stub 501.

Le hook Claude Code reste `olivares claude-hook` (PEP PreToolUse). C’est
observation et application, pas une CLI de remplacement.

## Permissions

Application existante sur les routes du module :

| Permission | Routes |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}`, `GET /runs/{ref}/messages` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume`, `POST /runs/{ref}/move`, `POST /runs/{ref}/handoff` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Un viewer peut lister. Create, input et stop exigent write. L’authorizer de la
route est le point d’application ; une permission manquante est 403.

## Routes lues par la console

Base : `/v1/m/sessions`. Authentifier. Envoyer `X-Olivares-Tenant`.

| Méthode | Chemin | Résultat |
|---|---|---|
| `GET` | `/runs` | Page d’exécutions gérées |
| `GET` | `/runs/{ref}` | Une exécution. `state` est dérivé. `exit_code` est observé. Pas de champ de succès fabriqué |
| `GET` | `/runs/{ref}/events` | Lignes de preuve de cycle de vie |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE : cadres `output`, `lag` si l’anneau a évincé sous le curseur, `end` ou un `notice` non vivant |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json utilise `line`/`message`. Codex/Grok utilisent `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM puis SIGKILL du groupe. Code de sortie observé sur la ligne |
| `POST` | `/runs/{ref}/resume` | Nouvelle génération de processus. Conversation stockée exacte |
| `POST` | `/runs/{ref}/interrupt` | Annule le tour actif. Le processus reste |
| `POST` | `/runs/{ref}/move` | Corps `{provider_profile_ref}`. Rattache une session Claude Code arrêtée ou en échec à un autre profil du même outil et emporte sa conversation. Ne démarre rien. 200 run |
| `GET` | `/runs/{ref}/handoff` | Le résumé dont partirait un handoff : `summary`, `source_driver`, `turns`, `transcript_bytes`. Lecture seule, consignée dans le ledger d’audit. Une session en cours d’exécution est lue telle qu’elle est ; une session qui n’a pas démarré répond 409 |
| `POST` | `/runs/{ref}/handoff` | Corps `{provider_profile_ref, summary?, name?}`. Démarre une nouvelle session dans ce profil, liée à celle-ci. 201 run plus `summary_state` (et `first_turn` lorsqu’il n’est pas `delivered`) ; 202 tant que le lancement attend une approbation |
| `GET` | `/runs/{ref}/activity` | Ce que fait l’outil d’une session Claude Code ou Codex, lu à chaque requête dans son propre enregistrement natif : `agents`, `tasks` (commandes et travail en arrière-plan), `files` (path, `added`, `removed`, `created`), `todos`, `skills`, `mcp_servers`, `model`, `context_tokens`, `context_window` nullable lorsque l’outil le rapporte et `context_percent` (pourcentage pour Claude Code, null lorsqu’il est inconnu), `compactions`, et `transcript` (`read`, `pending` avant que l’outil l’ait écrit, `missing` lorsqu’il devrait y être). Les agents et les tâches sont `running`, `completed`, `failed`, `stopped` ou `scheduled`. Codex utilise son fichier rollout sans lancer d’autre app-server ; les tokens de contexte décrivent la dernière requête, non l’usage cumulé. Lecture seule ; consignée dans le ledger d’audit une fois par lecteur et par session toutes les 15 minutes. Un outil non pris en charge répond 422 ; une session à laquelle des secrets du vault ou un identifiant de dépôt ont été donnés répond 409, tout comme une session qui a utilisé un jeton de registre de paquets lorsqu’elle est arrêtée ou s’exécute après une reprise qui suit un lancement en ayant utilisé un |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | Le transcript d’un sous-agent Claude Code en tours simples (`role`, `text`). Lecture seule, consignée dans le ledger d’audit. 404 pour un agent inconnu. Codex rapporte l’état des enfants dans la réponse Activity racine ; les lectures de transcript d’enfant ne sont pas offertes |
| `GET` | `/runs/{ref}/messages` | Les messages d’une session Claude Code ou Codex, lus à chaque requête dans le transcript propre à l’outil, par pages : `items` (`id`, `role` `user` ou `assistant`, `at`, `first_line`), `total`, `next`, `transcript` (`read`, `pending`, `missing`). Par défaut, seulement ce que la personne a envoyé ; `role=all` ajoute les réponses de l’outil. `q` garde les messages contenant cette expression n’importe où (insensible à la casse, 256 octets au plus) ; `at` (RFC 3339) garde les messages à cette heure ou après, de sorte que le premier élément est l’ancre ; `order` vaut `asc` (défaut) ou `desc`, `limit` au plus 200 et `cursor` l’id d’où une page continue (`next`) ; les éléments sont toujours du plus ancien au plus récent. `id` lit un message en entier (`text`, et `later_same`, le nombre de messages ultérieurs du même expéditeur qui répètent sa première ligne ; aucun filtre ne s’applique et `total` est le nombre de messages de la session). L’`id` d’un message est sa position parmi les messages de la session et ne change pas lorsque le transcript s’allonge. Lecture seule ; consignée dans le ledger d’audit une fois par lecteur, par session et par fenêtre de 15 minutes. Un outil non pris en charge répond 422 ; une session à laquelle des secrets du vault ou un identifiant de dépôt ont été donnés répond 409 ; un `id` inconnu 404 ; un paramètre invalide 400 |
| `GET` | `/runs/{ref}/diff` | Branche du worktree comparée à son point de départ : `branch`, `base`, `head` et `files` modifiés (`path`, `status`). 404 sans worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | Texte d’un chemin à `base` et `head` (`original`, `modified`, chacun limité au minimum de `max_read_bytes` du workspace et 64 KiB) |

Pour les appels Code Mode de Codex dont les arguments de plan ne sont pas conservés dans le rollout, Activity lit aussi la dernière notification `turn/plan/updated` déjà reçue par le pont actif. Un rechargement du navigateur utilise la même session côté serveur ; aucun app-server n’est lancé pour un rafraîchissement. Une fois le handle actif disparu, seules les données conservées par Codex lui-même peuvent être lues.

Pour Claude Code, la fenêtre provient du `modelUsage[model].contextWindow` natif de son cadre de résultat, conservé à travers l’arrêt et la reconnexion. Un rapport concordant et non ambigu fournit `context_percent` sous la forme `100 × context_tokens / context_window` ; sinon les deux champs valent `null`, et la CLI et la console affichent les tokens avec un avis de fenêtre inconnue. Un nouveau lancement de l’outil efface le rapport de fenêtre précédent.

Les tâches de workflow peuvent inclure `workflow` : `run_id`, `name`, `duration_ms`, `agents_total`, `agents_done`, `agents_failed` et `phases`. Chaque phase a `index`, `title`, `detail` facultatif et `state`, ainsi que les mêmes compteurs d’agents. Les compteurs décrivent les agents observés jusqu’ici. En direct, l’état d’une phase décrit les agents observés en cours d’exécution ou en échec. L’achèvement d’un agent ne prouve pas que le script a quitté sa phase ; il ne marque donc pas la phase comme terminée et n’identifie pas d’étape de script courante. Le résumé final du workflow fournit les états de phase terminés.

Les instantanés de workflow en direct proviennent de la sortie `system/task_progress` de Claude Code, partagée avec le flux attach. Le `workflows/<runId>.json` terminé de l’outil alimente le résumé final selon les règles de home, de lien et de taille du transcript. Les lectures de résumé sont bornées à 8 MiB par fichier et 64 MiB par requête ; des journaux ou résultats natifs plus volumineux rendent le résumé illisible tout en préservant la progression en direct disponible. Les résumés illisibles et les trous du tampon en direct borné contribuent à `unreadable`.

### Déplacer une session et la transmettre par handoff

Une session peut continuer dans un autre compte de deux façons, et la console ainsi que
`olivares session move` / `olivares session handoff` offrent les deux. `move` transporte la conversation
stockée d’une session Claude Code vers un autre profil du même outil ; seules les sessions Claude Code
peuvent donc être déplacées. `handoff` démarre une nouvelle session à partir de la conversation d’une
session Claude Code, Codex, Gemini CLI ou OpenCode.

Un run porte `limit` lorsque son outil a terminé le dernier tour sur une limite :
`{kind, resets_at, account_ref}`. `kind` vaut `usage` (l’allocation du plan du compte est
épuisée) ou `rate` (le fournisseur limite le débit des requêtes). `resets_at` est en
RFC 3339, présent lorsque l’outil indique quand la limite se lève et absent sinon, et
`account_ref` est le profil sous lequel le tour s’est exécuté. Claude Code fournit le fait
dans son propre résultat (`api_error: usage_limit_reached` ou HTTP 429) et l’heure de
réinitialisation dans son `rate_limit_event` ; Codex le fournit dans un tour en échec typé
`usageLimitExceeded` ou `rateLimitExceeded` et l’heure de réinitialisation dans ses fenêtres
`account/rateLimits/updated` ; lorsque deux fenêtres sont épuisées, c’est la réinitialisation
la plus tardive qui est consignée, et une heure de réinitialisation à plus d’un an d’ici est
omise. Le ledger gagne un événement `limit_reached` lorsque le fait est consigné pour la
première fois ; le même fait rencontré à nouveau n’en ajoute aucun.
Le prochain tour réussi efface `limit`, tout comme un déplacement vers un autre compte. Un
résultat de tout autre type ne le définit jamais.

`POST /runs/{ref}/move` conserve la même session. Il exige une session `stopped` ou `failed`
dont la conversation se trouve dans son home de compte, et un profil cible du même outil
qu’un lancement sous ce profil accepterait (actif, environnement de ce nœud, source
autorisée). Le moteur copie cette seule conversation, le transcript et ses fichiers de
sous-agents et de résultats d’outils, vers le home cible, sans jamais copier une connexion,
un réglage ou une autre conversation, et rattache à nouveau la ligne du run, la ligne live et
le ledger dans une seule transaction. Le run conserve son `run_ref` et son identifiant de
conversation fournisseur ; le ledger gagne un événement `moved` dont le détail nomme les deux
profils par référence et compte les fichiers transportés, et une session en échec conserve sa
`reason`. Le home source n’est que lu. Un refus n’écrit rien et retire ce que le déplacement
a créé :

- 409 pour une session en cours d’exécution, une session à laquelle ont été donnés des secrets
  du vault, un identifiant de dépôt ou un jeton de registre de paquets (on ne peut pas prouver
  que ce qu’elle a affiché en est exempt), une ligne dont le home n’appartient pas à son
  profil, un identifiant de conversation déjà lié à une autre session sous la cible, une cible
  qui détient une version différente de la conversation, une cible d’un autre outil, ou un
  fichier modifié pendant son transport (réessayez) ;
- 422 pour un lien, un second nom d’un fichier ou un fichier spécial dans la conversation, et
  pour la session d’un autre outil.

Une copie de la même conversation laissée sur la cible par un déplacement antérieur est
remplacée par la plus récente. Rien ne démarre : le `POST /runs/{ref}/resume` suivant est
l’appel ordinaire, de sorte que la politique, les budgets et les approbations du profil cible
s’y appliquent. Une session qui n’a pas encore de conversation se déplace sans rien à
transporter et redémarre sur la cible.

`GET /runs/{ref}/handoff` et `POST /runs/{ref}/handoff` continuent dans un autre outil, là où
aucune reprise native n’existe. Ils lisent la conversation stockée d’une session Claude Code, Codex, Gemini CLI ou OpenCode (une session
Grok répond 422). La session peut être `stopped`, `failed`, `running` ou `idle` ; une session qui n’a pas
démarré répond 409. L’aperçu lit une session en cours d’exécution telle qu’elle est. Le POST arrête d’abord
une session en cours d’exécution, comme le fait `POST /runs/{ref}/stop`, et lit sa conversation une fois son
outil terminé ; tout refus qui n’a pas besoin de l’arrêt (le profil cible, les secrets du vault, un
identifiant de dépôt) vient avant lui, et un refus après l’arrêt indique que la session a été arrêtée et peut
être reprise. Codex est lu depuis son rollout, Gemini CLI depuis son journal de chat et OpenCode par son
propre `opencode export`, lancé une fois et hors ligne sous le profil de la session. L’aperçu
est généré à partir de la conversation stockée sans modèle, afin que la personne puisse le
relire et le modifier, et chaque lecture en est consignée dans le ledger d’audit sous
`sessions.run.handoff_preview`. Le POST démarre une nouvelle session dans le profil choisi,
dans le même workspace et sous le même template, dont le premier tour est le `summary` relu
(généré s’il est absent) et le chemin du transcript, écrit sous le home de configuration du
profil cible sous un nom qui lui est propre. Le transcript porte les messages en entier
jusqu’à 256 KiB chacun, les appels et résultats d’outils abrégés, et indique combien
d’enregistrements il n’a pas pu lire. Le nouveau run indique `handoff_of`, et le ledger de
l’original gagne un événement `handed_off`. La réponse porte `summary_state` :

| `summary_state` | Signification |
|---|---|
| `delivered` | Le premier tour a atteint la nouvelle session. |
| `not_delivered` | Il n’a pas traversé. `first_turn` contient le texte à envoyer. |
| `unknown` | Il a été tenté et le résultat n’a pas été confirmé ; vérifiez avant de renvoyer `first_turn`. |
| `waits_for_approval` | Le lancement est retenu pour une personne (202) ; aucun premier tour n’est envoyé automatiquement. |

`first_turn` n’est présent que lorsque l’état n’est pas `delivered`, car il nomme le chemin de
la pièce jointe. La conversation source est refusée avec 409 lorsque sa session a reçu des
secrets du vault, un identifiant de dépôt ou un jeton de registre de paquets, parce qu’un
handoff l’envoie à un autre fournisseur, et avec 422 au-delà de 256 MiB ou lorsqu’elle
contient un seul enregistrement de plus de 32 MiB.

### Option worktree

`POST /runs` accepte le booléen facultatif `worktree`. S’il vaut true et que
`workspace_ref` désigne un workspace enregistré à la racine d’un dépôt Git, la
session travaille dans un nouveau worktree et une nouvelle branche de ce dépôt
(`workspace_path` est le worktree ; le run indique `worktree_branch`). Absent, null
ou false conserve le comportement existant. Le moteur répond 422 pour un workspace
sans worktree Git, hors de la racine du dépôt, sans commit, en lecture seule ou
avec des dossiers en lecture seule, dont la configuration Git nomme un filtre ou
inclut un fichier, pour une isolation non native ou un lancement sans workspace ;
503 si le nœud n’a pas de répertoire de worktrees. `POST /runs/{ref}/resume`
retourne dans le même worktree.

`POST /runs/{ref}/cleanup` accepte un body facultatif `{"discard_worktree": true}`.
Sans body, le comportement reste inchangé ; seul true explicite confirme.
Une session avec worktree est libérée si sa branche est fusionnée dans la branche
courante du workspace, que le worktree est sur cette branche et n’a aucun fichier
non commité. Sinon l’appel est refusé avec 409 (travail non fusionné, HEAD détachée,
worktree inaccessible) et le run reste arrêté. Réessayer avec `discard_worktree`
supprime malgré tout worktree et branche ; si le worktree est inaccessible, cela
libère la session en laissant le worktree en place. L’événement `cleaned` du ledger
consigne le résultat avec le sommet de branche.

### Approuver la commande de préparation d’un dossier

Une commande de préparation appartient au dossier enregistré, dans le stockage du moteur. Les
fichiers du dépôt et les variables d’environnement ne peuvent pas la modifier. Rien n’exécute
encore la commande dans ces opérations ni pendant le démarrage d’une session.

- `PUT /workspaces/{ref}/setup-command` prend `{"command":"npm ci"}`. Le texte UTF-8 exact
  est conservé, espaces compris ; au plus 8192 octets, sans NUL. Une chaîne vide efface la
  commande. Exige l’administration du workspace.
- `GET /workspaces/{ref}` renvoie `setup_command`, `setup_command_sha256` et, après
  approbation, `setup_approval` avec `sha256`, `actor` et `at`.
  `setup_approved` vaut true seulement tant que cette approbation couvre le texte actuel.
  Après une modification, `setup_approval` reste comme preuve ; testez donc `setup_approved`,
  jamais la présence de `setup_approval`.
- `POST /workspaces/{ref}/setup-command/approve` prend `{"sha256":"<reviewed hash>"}`.
  Elle exige l’administration du workspace et refuse une identité d’agent. Le hash
  doit correspondre au texte actuellement stocké ; une modification concurrente renvoie 409.
  L’approbation et son entrée d’audit `sessions.workspace.approve-setup` sont validées ensemble.

Un démarrage avec worktree renvoie 409 lorsque la commande n’a pas d’approbation
correspondante, avant de créer un worktree. Son message nomme la commande et l’étape
d’approbation. Modifier un seul caractère, espaces compris, exige une nouvelle approbation.
La dernière approbation reste comme preuve ; son hash doit être égal à `setup_command_sha256`
pour s’appliquer, de sorte que rétablir une commande dont le texte a été approuvé auparavant
applique de nouveau cette approbation. Quiconque peut lire le dossier peut lire la commande ;
n’y mettez donc aucun secret.
`POST /runs` avec `"no_setup":true` ignore la préparation pour ce seul démarrage. Un handoff
d’une session qui a un worktree démarre un nouveau worktree et exige la même approbation ; il
n’a pas d’option pour l’ignorer. Les autres démarrages et reprises conservent leur
comportement existant.

Avec la CLI, utilisez `olivares agent workspace setup <ref> --command 'npm ci'`,
lisez-la avec `olivares agent workspace setup <ref>`, et approuvez le hash affiché avec
`olivares agent workspace approve-setup <ref> --sha256 <hash>`.
`olivares session start --worktree --no-setup` ignore la préparation une seule fois.

### Démarrer un worktree à un commit ou une branche

Avec `worktree`, `POST /runs` accepte aussi `worktree_from`, facultatif : un
identifiant complet de commit (40 ou 64 chiffres hexadécimaux minuscules) ou une
branche locale du dépôt du workspace. Le nouveau worktree et sa propre nouvelle
branche démarrent là plutôt qu’au commit courant du workspace ; la branche nommée
et le checkout ne bougent pas. Un destinataire ouvre ainsi le travail d’un handoff,
dont le contenu peut porter `branch` et `sha`. Git résout la valeur en identifiant
complet de commit, le seul utilisé. Avant toute création, le moteur répond 422
pour un commit ou une branche absent, une expression de révision, une plage, une
option ou `worktree_from` sans `worktree`. Sans valeur, le lancement part du commit
courant du workspace comme auparavant.

`GET /runs/{ref}/diff` liste les chemins modifiés par la branche du worktree depuis
sa divergence du commit courant du workspace (`base` est leur base de fusion,
`head` le sommet de branche, tous deux des identifiants complets ; 200 chemins au
maximum, `truncated` indique s’il y en a davantage). `GET /runs/{ref}/diff/file?path=`
retourne le texte d’un chemin à `base` et `head`, vide si le fichier n’y existe pas.
Il lit les objets Git commitées avec des commandes plumbing : aucune modification
non commitée n’y figure, et la permission est celle de lecture du run. Les règles
de fichiers du workspace restent applicables : un chemin hors des sous-chemins
autorisés n’est pas listé et répond 404 ; une posture DLP qui refuse répond 403
(avec audit comme une lecture de fichier workspace) ; un fichier dépassant 16 MiB
répond 413. Une session sans worktree ou d’un autre tenant répond 404 ; une branche
disparue ou sans historique commun répond 409. `worktree_from` répond aussi 422
pour un commit qu’aucune branche, étiquette ou branche distante du dépôt ne retient.

Reconnecter après un attach coupé est `GET …/attach?from={last+1}` sur le
**même** processus vivant. Après perte de processus, attach dit que la session
n’est pas vivante. Resume démarre une nouvelle génération. Reconnect n’invente
pas un processus de remplacement (SDD R04).

### Configuration d’agent que porte un dépôt

Un dépôt cloné peut porter une configuration d’agent qui exécute un programme ou redirige le
trafic. Le moteur la traite comme une entrée non fiable lorsqu’une session démarre dans un
dossier. Il lit les fichiers ci-dessous, ne les exécute jamais, et ne collecte que les clés
listées. Une valeur vide (`{}`, `[]`, `""`, `false`, `null`) ne configure rien et n’est pas
signalée.

| Outil | Fichier | Signalé |
| --- | --- | --- |
| Claude Code | `settings.json` et `settings.local.json` dans le dossier `.claude` | `hooks`, `apiKeyHelper`, `statusLine`, `mcpServers`, `enableAllProjectMcpServers`, `enabledMcpjsonServers`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`, `fileSuggestion`, `enabledPlugins`, `extraKnownMarketplaces`, et les entrées `env` dont le nom figure dans la dernière ligne |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`, `notify`, `mcp_servers`, `model_providers`, `otel`, tout `*_base_url` de premier niveau, et les entrées `shell_environment_policy.set` dont le nom figure dans la dernière ligne ; les mêmes clés sous `profiles.<name>` |
| Gemini CLI | `.gemini/settings.json` | `hooks`, `mcpServers`, `toolDiscoveryCommand`, `toolCallCommand`, `proxy`, `tools.discoveryCommand`, `tools.callCommand`, `mcp.serverCommand`, `telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`, `.env` | les entrées dont le nom figure dans la dernière ligne |
| Noms d’environnement | | `NODE_OPTIONS`, `NODE_EXTRA_CA_CERTS`, `NODE_TLS_REJECT_UNAUTHORIZED`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `LD_AUDIT`, `DYLD_INSERT_LIBRARIES`, `BASH_ENV`, `PATH`, `PYTHONPATH`, `GIT_SSH_COMMAND`, `GIT_EXTERNAL_DIFF`, `SSL_CERT_FILE`, `CLAUDE_CODE_SHELL_PREFIX`, `ANTHROPIC_CUSTOM_HEADERS`, `AWS_ENDPOINT_URL*`, et tout nom se terminant par `_BASE_URL`, `_API_BASE`, `_ENDPOINT` ou `_PROXY` (`NO_PROXY` ne fait que restreindre un proxy et n’est pas signalé) |

Un fichier qui existe mais ne peut pas être lu dans son format (JSON ou TOML invalide, plus de
1 MiB, imbriqué sur plus de 64 niveaux, pas un fichier ordinaire) compte comme signalé, car on
ne peut pas démontrer qu’il est sûr. En TOML, chaque segment d’une clé pointée ou d’un en-tête
de table compte pour un niveau, comme dans le document analysé, et s’ajoute aux crochets qui
l’entourent : un en-tête de 30 segments sur 35 tables en ligne imbriquées fait 65 niveaux.
Dès qu’un fichier a un en-tête `[[array]]`, les segments d’en-tête comptent double, car chacun
peut nommer un tableau et son élément. Un fichier trop volumineux est haché en entier à chaque
contrôle. Un dossier sans rien de signalé démarre comme avant.

- `GET /workspaces/{ref}/project-config` liste les clés signalées par fichier avec leurs
  valeurs (un mot de passe dans une URL est masqué) et le `sha256` de cette seule partie
  signalée, sur les fichiers des trois outils, plus `approved` et la dernière `approval`.
  Il lit le dossier lui-même ; un worktree démarré depuis un autre commit peut porter
  d’autres fichiers suivis, et son refus nomme son propre hash.
- `POST /workspaces/{ref}/project-config/approve` prend `{"sha256":"<reviewed hash>"}`,
  exige l’administration du workspace et refuse une identité d’agent. L’approbation
  enregistre qui, quand et le hash, avec une entrée d’audit
  `sessions.workspace.approve-project-config` dans la même transaction.

Un démarrage ou une reprise d’un outil qui lit un fichier signalé renvoie 409 sauf si
l’approbation stockée est égale au hash actuel. Modifier un seul caractère d’une valeur
signalée, ou ajouter ou retirer une clé signalée, exige une nouvelle approbation ; modifier
des clés non signalées n’en exige pas. Il n’existe aucune option pour ignorer le contrôle.

Une session Claude Code qui porte les hooks du moteur est lancée avec `--setting-sources ""` ;
sur Claude Code 2.1.299, cette session ne charge aucun fichier de réglages de projet ou local
ni aucun `.mcp.json` de projet, de sorte qu’un tel démarrage n’est pas refusé. Un lancement
de Claude Code sans les hooks du moteur (remote control, ou un moteur sans point de terminaison
de hook) lit ces fichiers et est contrôlé. Codex n’honore `.codex/config.toml` que pour un
projet de confiance dans son home de compte et ignore le fichier d’un projet non fiable ; le
moteur ne peut pas interroger l’outil, il contrôle donc ce fichier et une personne
l’approuve une fois. Gemini CLI 0.64.0 démarré comme le moteur le démarre (`gemini --acp`)
ignore `.gemini/settings.json` et `.gemini/.env` d’un dossier auquel son home de compte ne fait
pas confiance, et, dans un dossier de confiance, exécute `tools.discoveryCommand`, les `hooks`
et les commandes `mcpServers` et prend l’URL de base depuis `.gemini/.env` ; le moteur contrôle
ces fichiers pour la même raison. Il charge aussi des fichiers de dossiers parents, que ce
contrôle ne lit pas.

Avec la CLI, utilisez `olivares agent workspace project-config <ref>` pour la lire et
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` pour l’approuver.

## Transport (Community)

Chaque CLI officielle est lancée sur le transport dont sa propre forme
d’operate a besoin ; `cliruntime.LaunchTransport` déclare lequel. Les trois
formes sont aujourd’hui des protocoles stdio, donc la racine de composition
câble `sessions.NewProcRunner()` : stdin, stdout et stderr sont des tubes et
restent des flux distincts.

**Claude Code refuse un terminal sur stdin.** Sa forme `--print` avec
stream-json répond `Error: Input must be provided either through stdin or as a
prompt argument when using --print` et sort 1 sans un seul cadre de protocole.
Un terminal est ce dont une forme interactive a besoin ;
`sessions.NewPTYRunner()` reste disponible sous Linux pour cela. L’isolation
container et sandbox reste refusée dans les deux cas.

Le contrat de driver vit dans `modules/sessions/cliruntime`. Types : `claude`,
`codex`, `grok`. La conformité s’exécute toujours contre un faux en processus
et contre un pair PTY local. Lorsque `claude` / `codex` / `grok` sont sur PATH,
un test distinct possède le binaire réel, l’arrête et enregistre la sortie. Il
n’envoie pas un tour de modèle.

## Pages connexes

- [Exploiter une session fournisseur](/how-to/operate-provider-sessions/)
- [Module II — opération en direct](/reference/modules/ii-sessions/)
- [Connecter Claude Code](/how-to/connect-claude-code/)
- [OpenAPI bêta](/reference/api-beta/)
