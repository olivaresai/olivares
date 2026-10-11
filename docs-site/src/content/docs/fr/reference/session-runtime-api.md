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
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume` |
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
| `GET` | `/runs/{ref}/diff` | Branche du worktree comparée à son point de départ : `branch`, `base`, `head` et `files` modifiés (`path`, `status`). 404 sans worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | Texte d’un chemin à `base` et `head` (`original`, `modified`, chacun limité au minimum de `max_read_bytes` du workspace et 64 KiB) |

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
