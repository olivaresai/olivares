---
title: "Votre première heure avec Olivares AI (v26.9.1, tel que livré)"
description: >-
  Ce qu’une installation propre du binaire public v26.9.1 vous permet
  réellement de faire pendant la première heure : jeton de configuration,
  barrière AAL3, enregistrement d’une passkey, lancement de session via un
  profil fournisseur, déploiement, connaissances et hooks PEP Codex et Grok.
---

Cette page décrit **v26.9.1 tel que livré**. Il ne s’agit ni d’un assistant de
premier démarrage prévu, ni d’une capture d’écran d’une maquette. Chacune des
étapes ci-dessous correspond à une action que le binaire public effectue
aujourd’hui, avec le fichier ou la variable d’environnement qui la rend
possible. Lorsque le produit refuse une action, la page le dit.

Les faits numérotés ont été mesurés le 2026-09-04 sur une installation propre
du binaire public. Cette page cite ces mesures et le code auquel elles
aboutissent.

Pour installer le binaire sur l’hôte, consultez
[Auto-héberger Olivares AI](/how-to/self-hosting/) et
[Vérifier une version](/how-to/verify-a-release/). Cette page ne vous demande
pas d’envoyer `https://olivares.ai/install` dans un shell par un pipe. Une fois
le binaire installé sur l’hôte, la première commande recommandée est
`olivares quickstart`.

:::note[Ce que ceci n’est pas]
`--seed-demo` n’est pas une visite guidée. Mesure effectuée le 2026-09-04 sur
une installation propre du binaire public : **36 routes sur 54** de la console
restent vides après un démarrage avec données de démonstration. Ce chiffre est
le recensement de la date de mesure. La
[référence console](/reference/console/) générée dans cet arbre liste
**75 routes**. Cette page ne recompte pas les onglets vides après
`--seed-demo` sur v26.9.1. L’environnement de démonstration alimente le
parcours du graphe d’accès dans
[De zéro à un graphe d’accès en lecture/écriture](/tutorials/zero-to-graph/),
mais pas le reste de la console. Ne l’utilisez pas pour « explorer le
produit ».
:::

## 1. Démarrage recommandé : `olivares quickstart`

Un nouveau répertoire de données ne contient **aucun identifiant par défaut**.
La première commande recommandée est `olivares quickstart`
(`cmd/olivares/cmd_quickstart.go`). Il s’agit de `serve` avec des valeurs par
défaut sûres : TLS activé, aucun identifiant par défaut et un token de configuration
à usage unique. L’adresse d’écoute par défaut est `:8443` — toutes les interfaces, car
c’est un serveur (`cmd/olivares/binddefaults.go`). Mesure
effectuée le 2026-09-04 sur une installation propre du binaire public avec un
véritable TLS (y compris une exécution sur **:8460**) ; le texte du panneau est
identique.

Le panneau de bienvenue (`announceQuickstart`, `:154-163`) est numéroté. Le
moteur affiche `https://localhost:8443` pour cette liaison, et liste sous le token
toutes les autres adresses auxquelles cet hôte répond. **N’utilisez pas une IP pour la cérémonie de la
passkey.** Le navigateur refuse une adresse IP comme RP ID WebAuthn
(`SecurityError`). Le produit dérive le RP ID du nom d’hôte de la requête
(`core/api/handlers_webauthn.go:33-50`). Avant d’enregistrer la passkey, ouvrez
la console à l’adresse `https://localhost:PORT` (ou avec un véritable nom
d’hôte), et non à `127.0.0.1`. `PORT` vaut `8443`, sauf si vous avez passé
`--listen`.

```text
=== WELCOME TO OLIVARES AI ===
  1. Open:   https://localhost:8443
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

La bannière affiche `localhost` pour la liaison par défaut et liste sous le token les
autres adresses de cet hôte ; une cérémonie passkey exige un nom, pas une adresse. Remplacez
l’hôte par `localhost` dans la barre d’adresse.

Le préfixe du jeton est `olst_` (`cmd/olivares/e2e_binary_test.go` recherche
`olst_[A-Z0-9]+`). La page de la console est `/setup`. L’API à laquelle
l’assistant envoie la requête est :

```http
POST /v1/setup
Content-Type: application/json

{"token":"olst_…","email":"you@example.com","password":"…"}
```

`olivares serve` affiche une bannière similaire (`announceSetup` dans
`cmd/olivares/cmd_serve.go`). Utilisez `quickstart` pendant la première heure :
l’URL, l’avertissement concernant le certificat autosigné et le jeton à usage
unique figurent dans un même panneau. Connectez-vous ensuite avec
`POST /v1/auth/login`. Vous disposez maintenant d’une session par mot de passe
AAL1.

`README.md` et ce panneau de bienvenue nomment le jeton et l’URL. Ils ne
mentionnent **pas** l’enregistrement d’une passkey. Celui-ci vient ensuite,
depuis la console, après que vous avez appuyé sur un bouton.

## 2. La barrière AAL3 — la console vous oriente après l’action, pas avant

Après la configuration, **la création de sources, de connecteurs, d’espaces de
travail et de secrets est refusée tant que la session n’a pas atteint AAL3**.
La barrière est `requireAAL3` dans `core/api/middleware.go:310`. Un principal
dont le niveau est inférieur à AAL3 reçoit `403
step_up_required`.
**21 sites d’appel** passent par cette barrière. Les chemins d’écriture de cet
arbre comprennent :

| Surface | Handler | Fichier |
|---|---|---|
| Ajout/suppression/rechargement du registre des sources | `handlePutSource` / `handleDeleteSource` / `handleReloadRuntime` | `core/api/handlers_sources.go` |
| Écriture et test des connecteurs | `handlers_connectors.go` | `core/api/handlers_connectors.go` |
| Ajout/suppression d’un secret | `handlePutSecret` / `handleDeleteSecret` | `core/api/handlers_secrets.go` |
| Création/mise à jour d’un espace de travail | `handleCreateWorkspace` / `handleUpdateWorkspace` | `core/api/handlers_scoping.go:152` / `:199` |
| Intégration d’un membre | `handleOnboardMember` | `core/api/handlers_onboarding.go:59` |

**PIV/CAC ne vous permet pas d’atteindre ce niveau sur une installation
standard.** Sans configuration, les routes PIV répondent **501**
`piv_not_configured` (`core/api/handlers_piv.go`, `core/api/errors.go`).

### Ce que fait réellement la console

La console vous envoie **bien** vers l’enregistrement. Elle le fait de manière
**réactive**.

1. Vous tentez une action privilégiée. Le panneau d’authentification renforcée
   affiche **S’authentifier avec une clé de sécurité**
   (`web/src/features/identity/i18n/en.json` `assurance.authenticate` ; bouton
   dans `web/src/features/identity/assurance.tsx:230-239`).
2. Ce clic appelle `POST /v1/auth/webauthn/authenticate/options`
   (authentification renforcée, pas enregistrement). Sans passkey, le moteur
   répond **400** `no_webauthn_credential` (`isNoWebAuthnCredential` dans
   `web/src/features/identity/api.ts:260-265`).
3. Le panneau vous demande alors de vous enregistrer d’abord dans l’onglet
   **Connexion privilégiée** (`assurance.tsx:160-168` →
   `assurance.unenrolled`). Cette phrase **n’est pas un lien**.

Vous devez vous y rendre manuellement :

1. Ouvrez la console à **`https://localhost:PORT`**, et non à `127.0.0.1`
   (voir §1).
2. Ouvrez `/identity` (`web/src/features/registry.tsx` —
   `path: '/identity'`).
3. Onglet **Connexion privilégiée** (`tabs.login`).
4. **Enregistrer une passkey** (`passkeys.register`). Un
   **authentificateur de plateforme suffit** (l’invite du navigateur ou du
   système d’exploitation ; aucune clé physique n’est nécessaire). Le serveur
   exige une **vérification de l’utilisateur**
   (`core/auth/webauthn.go:74-85`, `UserVerification: VerificationRequired`).
   Mesure effectuée le 2026-09-04 sur une installation propre du binaire public
   avec une cérémonie WebAuthn complète : enregistrement **200**,
   authentification `{"aal":3}`, puis `PUT /v1/console/connectors` **200**.

`POST /v1/auth/webauthn/register/options` est authentifié en tant que
**principal de session** et n’appelle **pas** `requireAAL3`. En cas de succès,
il écrit **200** avec `{publicKey: …}`
(`core/api/handlers_webauthn.go:76-88`). Terminez la cérémonie avec
`POST /v1/auth/webauthn/register`, puis réessayez l’authentification renforcée.

`README.md` et le panneau de bienvenue d’`olivares quickstart` ne mentionnent
**pas** l’onglet Connexion privilégiée. La console ne le nomme qu’**après**
cette réponse 400.

Le panneau d’identité présente AAL3 (NIST SP 800-63B-4) et PIV/CAC (FIPS 201-3)
comme des **normes cibles** et indique qu’il **ne revendique aucune
certification** (`targetStandardsNote`). Cette page ne revendique pas non plus
de certification.

### Ajouter un connecteur : la barrière, aucun champ

**Ajouter un connecteur** (`web/src/features/console/i18n/en.json`
`connectors.add`) ouvre une boîte de dialogue dans laquelle
`<RequireAssurance minAal={AAL.HARDWARE}>` entoure `ConnectorForm`
(`web/src/features/console/connectors-tab.tsx:348-357`). En dessous d’AAL3, le
formulaire n’est pas monté. Mesure effectuée le 2026-09-04 sur une installation
propre du binaire public : cette boîte de dialogue ne contient **aucun champ**
(`inputs: []`), uniquement le panneau d’authentification renforcée. Le catalogue
des types est visible à AAL1 (`ConnectorCatalog` se trouve en dehors de la
barrière, dans le même fichier `:341-346`) ; **ajouter** un connecteur ne l’est
pas.

### `/workspace` et liaisons de protocole : aucun sélecteur d’espace de travail

`/workspace` (`registry.tsx` `path: '/workspace'`) et
`/communications/protocol-bindings` nécessitent un espace de travail. Sur une
installation propre, vous n’en avez aucun et vous ne pouvez pas en créer avant
AAL3 (`handleCreateWorkspace`). `WorkspaceSwitcher` **n’est pas rendu** quand
il existe au plus un espace de travail
(`web/src/components/layout/workspace-switcher.tsx:43-44` :
`if (workspaces.length <= 1) return null`). La barre supérieure conserve
**Changer d’organisation** (`web/src/lib/i18n/locales/en/auth.json`
`tenant.switch`). Il n’existe aucun sélecteur d’espace de travail sur lequel
cliquer.

## 3. Lancer une session Claude Code depuis la console

La console ne peut lancer un processus `claude` que si l’**hôte** dispose d’une
source d’identifiants d’inférence. Sans cette source, les lancements stream-json
sont refusés en mode fermé par défaut (deny-closed). Mesure effectuée le
2026-09-04 sur une installation propre du binaire public : **HTTP 503**.

Définissez **une** des variables suivantes :

- `OLIVARES_SESSION_RUNTIME_WIF` — émission WIF dans le processus (`cmd/olivares/sessionruntime.go`, `cmd/olivares/wifbroker.go`)
- `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` — chemin d’un fichier de jeton renouvelé et de courte durée

La racine de composition journalise la source câblée ou, à défaut :

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go:74-76`). Variables apparentées facultatives :
`OLIVARES_SESSION_RUNTIME_WIF_RULE`, `OLIVARES_SESSION_RUNTIME_TOKEN_TTL`,
`OLIVARES_SESSION_RUNTIME_BASE_URL`, `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`
(répertoriées dans `cmd/olivares/config_registry.go` et dans
[Configuration](/reference/configuration/)).

Les topologies de codéploiement **Operate** (sur le même hôte que `claude`)
sont décrites dans
[Exécuter Claude Code avec Olivares](/how-to/run-claude-code-with-olivares/).
Le chemin d’observation OTLP est présenté dans
[Connecter Claude Code](/how-to/connect-claude-code/).

### Les clés de fournisseur ne lancent pas une session

**Modèles → Clés de fournisseur** est un registre de gouvernance de
**références**. Le formulaire **n’accepte jamais de secret** :

> Ce formulaire n'accepte jamais de secret. Olivares ne stocke qu'une
> référence et un indice masqué.

(`web/src/features/models/i18n/en.json` `keys.dialog.noSecretNote`). Remplir cet
onglet ne satisfait ni `OLIVARES_SESSION_RUNTIME_WIF` ni
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`. Cela n’active pas les lancements.

## 4. Le plan de déploiement répond 503 jusqu’à ce que vous provisionniez un exécuteur

Les opérations plan/apply par `POST` du module de déploiement renvoient **503**
jusqu’à ce que l’hôte définisse `OLIVARES_DEPLOY_EXECUTOR_CONFIG` sur un
fichier JSON. Si cette variable est absente, le module conserve l’exécuteur non
câblé en mode deny-closed (`cmd/olivares/deployexec_load.go:16-20`). Un fichier
illisible fait **échouer le démarrage**.

L’objet JSON est `deployExecutorConfig` dans
`cmd/olivares/deployexec_load.go:26-42`. Blocs backend facultatifs :
`tofu`, `terraform`, `gitops`, `k8s`, `docker`, `nomad`, `crossplane`, ainsi que
`credential`, `blast_radius`, `identity_binding`, `drift`.

La variable d’environnement est documentée dans
[Configuration](/reference/configuration/)
(`docs-site/src/content/docs/reference/configuration.md:152`). Elle n’est pas
documentée comme une action à effectuer dans la console pendant la première
heure. L’interface utilisateur ne propose aucun choix qui remplace ce fichier.

Le catalogue des modules marque l’actuation de Deployment comme
**on-demand (503)** ([Modules](/reference/modules/overview/)). Cette ligne
exprime le même fait.

## 5. Interroger une base de connaissances publique ou utiliser une identité d’agent

L’embedder par défaut est **LocalHashEmbedder**, sans trafic sortant. Au
démarrage, un avertissement indique que la récupération est **lexicale, et non
sémantique**, et affiche `embed_model=local-hash`
(`cmd/olivares/claude_inference.go`, `cmd/olivares/knowledgestatus.go`). La
récupération lexicale renvoie tout de même des fragments lorsque la barrière
les autorise.

Sans identité d’agent authentifiée, la barrière de récupération n’accorde que
du contenu **public et sans restriction** (`modules/knowledge/query.go:120-127`).
Une requête REST humaine à `/query` sur une base de connaissances **interne**
est refusée. Mesure effectuée le 2026-09-04 sur une installation propre du
binaire public : une base de connaissances **publique** a renvoyé **1 résultat**
(score 0.738). Interrogez une base de connaissances publique ou utilisez une
identité d’agent.

Aujourd’hui, une requête refusée indique `excluded_chunks: 0` même lorsque tout
a été exclu. Ce compteur n’augmente que pour le seuil `excluded_sources` défini
par l’opérateur (`query.go:256-284`) ; un refus lié à l’habilitation ou à l’ACL
ne l’incrémente jamais.

## 6. Sessions Codex et Grok : profils fournisseur, puis les hooks CLI restants

v26.9.1 exploite la CLI officielle Codex et la CLI officielle Grok comme
pilotes de session, en plus de Claude Code (`CHANGELOG.md` `[26.9.0]` Added).
La console administre ces lancements sur **Provider profiles**
(`/provider-profiles`, `sessions:profile:read`) et **Source bindings**
(`/provider-bindings`, `sessions:profile-binding:read`). Les deux routes sont
dans la [référence console](/reference/console/) générée.

Un profil est l’identité durable d’une instance fournisseur configurée sur un
environnement d’exécution. Ce n’est pas un compte fournisseur authentifié
(`web/src/features/agentops/types.ts`). L’enregistrement valide des homes qui
existent déjà sur ce nœud. Le serveur n’installe, ne crée ni ne se connecte.

### Enregistrer le pilote sur l’hôte avant un lancement

La préparation est par pilote. Il n’y a pas d’interrupteur partagé
(`cmd/olivares/sessionruntime.go`). La variable d’environnement correspondante
enregistre ce pilote sur ce nœud :

| Pilote | Variable d’environnement | Si absente |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (défaut `claude`) | le chemin Claude utilise le nom par défaut |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | les profils Codex restent observables et ne sont pas lançables |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | les profils Grok restent observables et ne sont pas lançables |

Les valeurs sont des binaires officiels épinglés. Le moteur ne résout pas
`codex` ni `grok` depuis `PATH`. Source : [Configuration](/reference/configuration/).

Les lancements Claude exigent encore une source de jeton d’inférence, comme au
§3. Codex et Grok s’authentifient avec le `auth_source` AUTORISÉ du profil
(`provider_account_home` ou `managed_injection`, sans repli).
`CHANGELOG.md` `[26.9.0]` n’affirme **pas** la compatibilité avec un compte
Grok officiel authentifié.

Le dialogue de lancement exige un profil fournisseur. L’extrémité productive
de création exige `provider_profile_ref`. L’omettre conserve l’ancien corps,
que cette API refuse ([CLI](/reference/cli/)
`olivares agent session create --provider-profile`).

Enregistrer un profil, lier une source, lancer, interrompre et arrêter :
[Exploiter une session fournisseur](/how-to/operate-provider-sessions/).

### Ce qui reste en CLI seulement (hooks et configuration gérée)

Ces commandes ne sont pas le lancement de session console. Elles existent
toujours :

| Commande | Fonction | Source |
|---|---|---|
| `olivares codex` | Produit les fichiers Codex `requirements.toml` / `managed_config.toml` à partir d’un JSON Policy. **Écrit des fichiers ; ne communique pas avec le plan de contrôle.** | `cmd/olivares/cmd_codexmanagedconfig.go` |
| `olivares codex-hook` | Hook PEP deny-closed invoqué par Codex (stdin → plan de contrôle → stdout dans la forme de l’événement). | `cmd/olivares/cmd_codexhook.go` |
| `olivares grok-hook` | Hook PEP deny-closed invoqué par Grok Build. Un refus ne **bloque** que lors de `pre_tool_use`. | `cmd/olivares/cmd_grokhook.go` |

Pour installer le hook Codex (forme du `hooks.json` de Codex vérifiée dans le
commentaire de ce fichier), `command` doit être une **chaîne**,
`olivares codex-hook`. Environnement :
`OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT` (agent/organisation/compte facultatifs).

Environnement du hook Grok : `OLIVARES_GROK_HOOK_URL`,
`OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`. Grok peut désactiver
un hook par son nom via `~/.grok/disabled-hooks` ; ce n’est pas un contrôle de
la console.

Ne cherchez pas de **bouton** « Connecter Codex » ou « Connecter Grok ».
L’enrôlement de connecteur est **Control console → Connectors** (type `codex`
ou `grok`). C’est le plan observer/gouverner dans
[Intégrer Codex](/how-to/integrations/codex/) et
[Intégrer Grok Build](/how-to/integrations/grok/). Cela n’enregistre pas un
pilote de session.

## 7. `--seed-demo` ne remplit pas la console

`olivares serve --seed-demo` charge un environnement de démonstration afin de
permettre l’exécution du tutoriel sur le graphe d’accès. Mesure effectuée le
2026-09-04 sur une installation propre du binaire public : **36 écrans sur 54**
de la console restent vides dans cet environnement. Ce chiffre est le
recensement de la date de mesure ; la référence console générée liste aujourd’hui
**75 routes**. Utilisez `--seed-demo` uniquement pour le parcours décrit dans
[De zéro à un graphe d’accès en lecture/écriture](/tutorials/zero-to-graph/).
Ne considérez pas les onglets vides après `--seed-demo` comme une installation
défectueuse, ni l’option comme une visite du produit.

## Pages connexes

- [Honnêteté et limites](/start/honesty-and-limits/) — ce que la documentation est autorisée à affirmer.
- [Auto-héberger Olivares AI](/how-to/self-hosting/) — comment exécuter le binaire.
- [Exploiter une session fournisseur](/how-to/operate-provider-sessions/) — profils, épinglage du pilote, lancement, interruption.
- [Configuration](/reference/configuration/) — les variables d’environnement nommées ci-dessus.
- [Modules](/reference/modules/overview/) — actuation on-demand (503) ou active.
