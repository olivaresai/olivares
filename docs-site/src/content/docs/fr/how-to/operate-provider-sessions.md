---
title: Exploiter une session de fournisseur
description: >-
  Choisissez un outil d’IA et son compte ou son fournisseur, travaillez dans un
  dossier de projet, approuvez des actions, interrompez un tour, arrêtez et
  reprenez depuis la console ou la CLI.
---

Suivez d’abord [Votre première heure](/how-to/first-hour/) avant de commencer ici.
Les outils s’exécutent sur l’hôte du moteur, y compris dans le conteneur pour une
installation Compose. La connexion et les dossiers de votre terminal client n’y sont
pas disponibles automatiquement.

## Choisir comment l’outil se connecte

**Outils** (`/agent-tools`) installe les outils et exécute leurs propres parcours de
connexion. Sur un moteur natif, l’instance d’outil par défaut utilise la connexion de
l’utilisateur du moteur ; les comptes connectés via Olivares restent des instances
distinctes. Sélectionnez le compte que vous voulez utiliser. Le relevé du compte
affiche l’adresse e-mail, le forfait, les fenêtres d’utilisation et les modèles que
l’outil expose. Les valeurs manquantes restent indisponibles, et les actualisations
échouées sont marquées comme périmées.

**Clés d’API** dans **Outils** (`/agent-tools`) contient les clés d’API et les
points de terminaison de modèles locaux. Ajoutez et testez le fournisseur à cet
endroit, puis utilisez-le depuis Nouvelle session. Une clé de fournisseur et un
abonnement à un outil sont deux façons différentes de se connecter. Ollama nécessite
un point de terminaison accessible et un modèle, sans clé d’API. Voir
[Ajouter un fournisseur](/how-to/add-a-provider/).

Pour les profils Claude avec `managed_injection` qui ne désignent aucun
fournisseur, `OLIVARES_SESSION_RUNTIME_WIF` ou `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
fournit l’identifiant d’inférence à l’échelle de l’hôte. Un profil lié à un
enregistrement de **Fournisseurs** utilise cet enregistrement à la place ; si son
identifiant ne peut pas être ouvert, le lancement est refusé sans repli sur
l’identifiant de l’hôte. Un profil `provider_account_home` utilise la connexion
autorisée de son outil et n’a besoin d’aucune de ces variables. Voir
[où s’appliquent les variables d’environnement](/how-to/add-a-provider/#the-environment-variables-and-where-they-still-apply).

## Démarrer et dialoguer

Ouvrez **Sessions → Nouvelle session** et choisissez un outil prêt. Le moteur
renseigne le profil et le dossier. Pour l’installation Compose du README, choisissez
**Modifier le dossier** et saisissez `/project` pour travailler sur votre projet hôte
monté. Conserver le dossier par défaut ne nécessite aucun chemin saisi. Laissez
**Premier message** vide pour démarrer, puis saisissez dans la conversation, ou
saisissez-le avant d’appuyer sur **Démarrer**.

Un outil qui utilise une clé de fournisseur emploie son modèle enregistré ou vous
laisse sélectionner un modèle renvoyé par le test de connexion. Si le modèle est
requis mais que le test n’en a renvoyé aucun, testez d’abord la connexion sous
**Clés d’API** dans **Outils**. Claude Code peut utiliser son modèle par défaut natif.

Pour l’installation Compose du README, exécutez la CLI à l’intérieur du conteneur du
moteur. Elle y trouve le moteur local et son certificat. Connectez-vous avec le compte
administrateur Olivares que vous avez créé pendant la configuration, et non avec votre
abonnement à l’outil.

Lorsque vous utilisez une clé de fournisseur avec un outil qui exige un modèle, comme
Codex, enregistrez un modèle par défaut sous **Clés d’API** dans **Outils** avant
d’exécuter `session start` ci-dessous. Si ni le fournisseur ni le profil de l’outil
n’a de modèle configuré, ajoutez `--model <tested-model-id>` à la commande
`session start`, en remplaçant l’espace réservé par un identifiant de modèle renvoyé
par le test de connexion. Un modèle sélectionné dans **Nouvelle session** ne s’applique
qu’à ce lancement depuis la console ; il ne définit pas le modèle d’un lancement par CLI.

Définissez le fichier Compose une fois dans le terminal de votre hôte :

```sh
export COMPOSE_FILE="$HOME/olivares/deploy/compose/docker-compose.yml"
docker compose exec olivares olivares login
docker compose exec olivares olivares tool ls
docker compose exec olivares olivares tool providers
docker compose exec olivares olivares session start /project --name first-hour
docker compose exec olivares olivares session send first-hour "Explain this project."
```

Pour une installation native, exécutez les mêmes commandes Olivares directement sur
l’hôte du moteur et remplacez `/project` par le chemin de votre projet. Le client
affiche l’outil et la connexion qu’il a choisis. Avec plusieurs outils, sélectionnez-en
un avec `--tool` ; `--profile` sélectionne un profil existant. Fermer le terminal client
laisse la session en cours d’exécution sur le moteur. Pour vous reconnecter à sa sortie :

```sh
docker compose exec olivares olivares session follow first-hour
```

## Approuver, interrompre, arrêter et reprendre

Sous **Plus d’options**, **Supervisé** fait demander à l’outil une
confirmation avant d’agir. Dans **Règles**, la demande qui attend votre décision est
ouverte à côté de la file d’attente, avec sa commande, ce qu’elle atteint et la règle
qui l’a déclenchée. Choisissez **Approuver une fois** (`A`), ou **Rejeter** (`D`) puis
ajoutez une note. Un lancement qui exige une approbation de l’organisation attend aussi
à cet endroit ; envoyez votre message une fois le lancement approuvé.

Utilisez **Interrompre** pour terminer le tour en cours tout en laissant la conversation
ouverte. Utilisez **Arrêter** pour terminer le processus, en confirmant si cela vous est
demandé. **Reprendre** relance cette même session avec le même profil, le même compte et
le même dossier ; il revérifie les identifiants et la politique. Envoyez un nouveau
message après la reprise.

```sh
docker compose exec olivares olivares session interrupt first-hour
docker compose exec olivares olivares session stop first-hour
docker compose exec olivares olivares session resume first-hour
docker compose exec olivares olivares session send first-hour "Continue explaining this project."
```

Un compte modifié ou indisponible entraîne un refus de lancement, et non l’autorisation de
passer à une autre connexion. Un tour Grok interrompu utilise l’annulation ACP sans accusé
de réception ; attendez le résultat corrélé du tour avant de le considérer comme terminé.

## Réglages facultatifs de profil et de git

Pour un répertoire de configuration d’outil configuré et existant, l’onglet **Profils de
fournisseur** de **Sessions** (`/sessions?tab=profiles`) permet à un administrateur
d’enregistrer un profil et de vérifier la disponibilité au lancement. Le profil et les
liaisons de sources d’un compte nommé figurent aussi dans son détail sous **Outils**. Ces
répertoires doivent exister sur l’hôte d’exécution. Utilisez **Plus d’options → Options de
lancement avancées** dans Nouvelle session pour sélectionner ce profil. Les remplacements
de binaire choisissent un exécutable ; sinon, le moteur essaie la dernière installation
gérée vérifiée, puis l’outil présent dans son `PATH`. Voir
[Configuration](/reference/configuration/).

Pour le dossier racine d’un dépôt Git ayant au moins un commit, **Travailler dans un
nouveau worktree git** crée une branche et un répertoire de travail pour la session. La
reprise les réutilise. Un handoff avec une branche et un commit propose **Ouvrir dans un
nouveau worktree de session** ; choisissez un dossier enregistré dont le dépôt contient ce
commit. Le moteur refuse les réglages de dépôt non pris en charge ou dangereux avant de
créer le worktree.

Un dossier peut stocker une commande de préparation pour les nouveaux worktrees. Une
personne doit approuver son texte exact, par SHA-256, avant qu’un démarrage de worktree
soit accepté ; modifier un seul caractère exige une nouvelle approbation. La commande
approuvée s’exécute une fois dans le nouveau worktree avant le premier tour de la session ;
un échec arrête le démarrage et affiche sa sortie. Si la préparation échoue, la reprise
exécute la commande à nouveau ; après une préparation réussie ou un démarrage avec
`--no-setup` enregistré, elle ne le fait pas. Voir
[Approuver la commande de préparation d’un dossier](/reference/session-runtime-api/#approving-a-folders-setup-command).

Le nettoyage refuse de supprimer un travail non commité ou non fusionné. **Supprimer
aussi le worktree et sa branche** autorise explicitement cette perte ; les fichiers
ignorés sont supprimés avec le worktree. Les worktrees partagent les métadonnées git du
dépôt. Voir la [référence CLI des sessions](/reference/cli/#command-olivares-session-start)
pour les options de worktree et l’[API d’exécution de session](/reference/session-runtime-api/)
pour les permissions de profil, les appels de cycle de vie et les limites d’isolation.
