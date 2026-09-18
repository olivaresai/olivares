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
v26.10 ajoute le contrat de driver, un runner PTY local et les parcours J01–J08
comme tests.

## Frontière d’édition

| Édition | Ce qu’elle fait | Ce qu’elle ne fait pas |
|---|---|---|
| **Community (cette page)** | Enfant local possédé : lancement, stdin/stdout/stderr, attach avec curseur, reprise de la conversation exacte, reconnexion d’un flux vivant, arrêt avec code de sortie observé. Les lignes de session et les preuves restent dans le module II. | Moteur Identity & Scale multi-panneaux, listener mTLS, sessions d’entrée commerciales, chunk xterm |
| **Overlay Identity & Scale** | Moteur commercial session-cockpit (listener, panneaux, ledger). Routes sous `/v1/m/session-cockpit/` lorsque l’add-on est présent. | Ne remplace pas `/v1/m/sessions/runs` |

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
