---
title: Ajouter un fournisseur et lancer Claude Code, Codex ou Grok
description: >-
  Enregistrez une clé d'API auprès du plan de contrôle, testez la connexion sans
  rien dépenser, liez-la à un profil de fournisseur et lancez la première
  session — depuis la console et depuis la CLI.
---

Cette page est la première heure du plan **fournisseur** : où va votre clé d'API,
comment vous savez qu'elle fonctionne, et comment une session démarre avec elle.

Dans la 26.10, une variable d'environnement du serveur reste une réponse à la première question, et ces
variables fonctionnent toujours. Elles ne sont plus le seul
chemin, et ce n'est plus ainsi qu'une nouvelle opératrice commence.

## Ce que veulent dire les trois mots

Une phrase pour chacun, parce que le produit les confondait et que les refus qu'il
vous donne les nomment séparément.

| Mot | Ce que c'est |
|---|---|
| **Fournisseur** | Une clé d'API ou un point de terminaison de modèle local, une `base_url` facultative et son type (`anthropic`, `openai`, `xai`, `gemini`, `openai_compatible`, `ollama`). |
| **Profil de fournisseur** | Une identité sur cette machine : quelle CLI officielle tourne, et sous quel répertoire de configuration et quel répertoire personnel. |
| **Session** | Un processus enfant lancé, sous un profil, avec l'identifiant d'un fournisseur. |

## 1. Ajoutez le fournisseur

### Depuis la console

1. Ouvrez **Fournisseurs** (IA → Environnements → Fournisseurs).
2. Choisissez **Ajouter un fournisseur**.
3. Choisissez le fournisseur, donnez-lui un nom que vous reconnaîtrez dans un
   sélecteur, et collez la clé. Laissez le point de terminaison vide sauf si vous
   pointez vers votre propre passerelle ; un fournisseur `openai_compatible` en exige
   un, car il n'y a aucun point de terminaison officiel à supposer.
4. Confirmez.

Le moteur scelle la clé au repos et renvoie un indice de quatre caractères. **La clé
n'est plus jamais renvoyée**, pas même juste après l'avoir écrite. Si vous la perdez,
changez-la : aucune lecture ne la récupère.

### Depuis la CLI

```sh
# La clé est lue sur stdin. Elle n'est jamais la valeur d'un flag : un flag dépose
# l'identifiant dans l'historique du shell et dans la table des processus.
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt

# Ou depuis une variable d'environnement de votre propre shell :
ANTHROPIC_KEY=sk-ant-... olivares provider add \
  --kind openai --name "Codex" --key-env ANTHROPIC_KEY
```

## 2. Testez la connexion

```sh
olivares provider test prv_01J8ABCDEF
```

Le test demande au fournisseur quels modèles il sert. **Il n'envoie aucune complétion
et ne dépense rien.**

Il répond l'une de trois choses, et ce sont trois questions différentes :

| Résultat | Ce que cela veut dire | Que faire |
|---|---|---|
| `ok` | Le fournisseur a répondu et a accepté l'identifiant. | Rien. Liez-le. |
| `refused` | Le fournisseur a répondu et a rejeté l'identifiant. | Changez la clé. |
| `unreachable` | Aucune réponse n'a été obtenue. | Vérifiez le point de terminaison, le réseau et tout proxy. **Cela ne dit rien sur la clé** : ne la régénérez pas. |

Un fournisseur que vous n'avez pas testé est affiché comme **non testé**, jamais comme
fonctionnel. Enregistrer un identifiant est une intention ; un test est un fait.

## 3. Enregistrez un profil et liez le fournisseur

Les chemins de répertoires que vous indiquez doivent déjà exister sur la machine
qui exécute le plan de contrôle. Le serveur les valide là-bas et ne crée pas les
répertoires indiqués qui manquent : un répertoire vide de secours donnerait à une
session une identité de fournisseur que personne n'a configurée.

```sh
olivares agent profile create \
  --driver claude \
  --config-home /home/ops/.claude \
  --user-home /home/ops \
  --name "Claude (travail)" \
  --auth-source managed_injection \
  --provider prv_01J8ABCDEF
```

`--auth-source` décide d'où vient l'identité de fournisseur de l'enfant, et les deux
valeurs ne forment pas une chaîne de repli :

- `provider_account_home` : la connexion déjà enregistrée dans les répertoires du
  profil. Olivares n'injecte rien et ne lit jamais ce fichier.
- `managed_injection` : un identifiant fourni par le moteur. Avec un fournisseur lié,
  c'est celui de ce fournisseur.

Le raccourci combine la détection, l'enregistrement et la liaison au fournisseur.
Si ni `--config-home` ni `--user-home` n'est fourni, le moteur gère les répertoires
du profil. Sans `--provider`, il choisit le profil qu'une nouvelle session du pilote
utiliserait ; avec `--provider`, il crée un profil avec ses propres répertoires :

```sh
olivares agent deploy claude --provider prv_01J8ABCDEF
```

Installez l’outil dans **Outils d’IA** avant d’utiliser ce raccourci. Il ne vous
connecte pas à votre compte fournisseur.

Pour lier (ou relier) plus tard :

```sh
olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ
```

Le moteur refuse un identifiant que le pilote du profil ne peut pas lire. Une clé
OpenAI sur un profil Claude est un refus qui nomme les deux, au moment de la liaison
puis de nouveau au lancement — pas une session qui échoue au milieu d'une poignée de
main.

| Type de fournisseur | Pilotes qui le lisent | Avec une `base_url` personnalisée |
|---|---|---|
| `anthropic` | `claude`, `opencode` | `claude` |
| `openai` | `codex`, `opencode` | `codex` |
| `xai` | `grok`, `opencode` | `grok` |
| `gemini` | `gemini-cli` |  |
| `openai_compatible` | `codex` | `codex` |
| `ollama` | `codex`, `opencode` | `codex`, `opencode` |

Pour `anthropic`, `openai` et `xai`, OpenCode accepte uniquement le point de terminaison du fournisseur : laissez `base_url` vide. Utilisez `codex` pour un fournisseur `openai_compatible` ; utilisez `codex` ou `opencode` pour `ollama`.

## 4. Lancez la première session

```sh
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent session create \
  --name acme-1 \
  --workspace ws-123 \
  --provider-profile ppf_01J8ZZZZZZ
olivares agent session attach run-123
```

`--provider-profile` sélectionne explicitement un profil. Lorsque les lancements
avec profil sont activés, son omission laisse le moteur résoudre un profil pour
Claude Code, l'outil par défaut : il réutilise ou crée un profil pour la connexion
propre à l'outil ou, à défaut, pour un enregistrement de fournisseur compatible.
Cela nécessite `sessions:profile:write` ; sans cette permission, sélectionnez un
profil explicitement. Les refus lors de la résolution du profil et du lancement
restent applicables.

Depuis la console, le même chemin est **Prise en main → Agents et première session**,
ou **Sessions → Nouvelle session**.

## Rotation et révocation

```sh
olivares provider rotate prv_01J8ABCDEF < nouvelle-cle.txt   # rescelle sur place
olivares provider rm prv_01J8ABCDEF --yes                    # irréversible
```

La rotation remplace la valeur sous la même référence, donc tous les profils liés
continuent de fonctionner et le **prochain** lancement utilise la nouvelle clé. Une
session déjà en cours garde l'identifiant avec lequel elle a démarré. Le test de
connexion précédent est effacé : un verdict mesuré sur un identifiant qui n'existe plus
ne prouve rien sur celui qui le remplace.

La révocation détruit la valeur scellée et refuse **en le nommant** tout lancement
futur. L'enregistrement et les liaisons sont conservés exprès : un profil qui cesserait
silencieusement de nommer quoi que ce soit se lirait comme un profil que personne n'a
configuré. Révoquer ici ne révoque pas la clé chez le fournisseur ; faites-le dans sa
propre console.

## Ce que le moteur refuse, et pourquoi

| Vous voyez | Cela veut dire |
|---|---|
| `provider credentials cannot be stored on this deployment` | Aucun coffre scellé n'est câblé. Le moteur refuse de stocker une clé plutôt que d'en stocker une qu'il ne peut pas protéger. |
| `the provider connection test is not available` | Aucune sonde n'est câblée. **Le lancement n'est pas affecté.** |
| `a … credential is not readable by driver …` | Le type et le pilote ne correspondent pas. Voir le tableau ci-dessus. |
| `the provider this profile is bound to is revoked` | Liez un fournisseur actif. |
| `this launch has two endpoints` | La passerelle d'inférence du déploiement et le `base_url` propre du fournisseur s'appliquent tous les deux. Retirez-en un ; le moteur ne choisit pas. |
| `the registered provider credential could not be opened` | Le lancement est refusé. **Il ne retombe pas sur un identifiant de l'hôte** : cela ferait tourner votre session sur un compte que vous n'avez pas choisi. |

## Les variables d'environnement, et où elles s'appliquent encore

Pour les profils Claude avec `managed_injection` qui ne désignent aucun fournisseur,
`OLIVARES_SESSION_RUNTIME_WIF` ou `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` fournit
l’identifiant d’inférence de l’hôte. Un profil lié à un fournisseur utilise son
identifiant ; en cas d’échec, le lancement est refusé sans repli vers celui de l’hôte.
Un profil avec `provider_account_home` utilise la connexion autorisée de l’outil
et n’a besoin d’aucune des deux variables.

## Voir aussi

- [Exploiter une session fournisseur](/fr/how-to/operate-provider-sessions/)
- [Votre première heure](/fr/how-to/first-hour/)
- [Référence de la CLI](/fr/reference/cli/)
