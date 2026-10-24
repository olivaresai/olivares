---
title: "Votre première heure avec Olivares AI"
description: >-
  Installez Olivares AI, connectez-vous avec un outil d’IA ou connectez un
  fournisseur, démarrez une conversation dans le dossier de votre projet,
  approuvez une action, puis arrêtez et reprenez la session.
---

Il vous faut Docker Compose, un dossier de projet sur lequel l’outil doit
travailler, et soit un abonnement à un outil, soit une clé d’API de fournisseur,
soit un modèle local.

## 1. Installer et ouvrir la console

Suivez l’[installation décrite dans le README](https://github.com/olivaresai/olivares#install).
Elle sélectionne explicitement l’image de publication, monte votre projet hôte
sur `/project`, et affiche une adresse de console ainsi qu’un jeton de
configuration à usage unique de remplacement. Utilisez-y les instructions
AppArmor lorsqu’elles s’appliquent à votre hôte Docker.

Ouvrez l’adresse affichée. La première installation utilise un certificat
auto-signé ; vérifiez que vous ouvrez bien votre propre serveur avant d’accepter
l’avertissement du navigateur. Saisissez le jeton de configuration, votre adresse
e-mail et un mot de passe pour créer le premier administrateur. Gardez le jeton
et le mot de passe confidentiels. La console vous connecte et ouvre l’assistant
de configuration.

Le conteneur exécute les outils sur le serveur. Un outil connecté sur votre
ordinateur portable correspond à une autre installation ; sa connexion n’est pas
copiée dans le conteneur.

## 2. Choisir un outil et sa connexion

Utilisez l’assistant ou ouvrez **Outils** (`/agent-tools`). Chaque outil indique
s’il est installé et connecté. S’il manque, examinez son installation et
approuvez-la. Utilisez la commande de connexion de l’outil et suivez son propre
parcours de connexion. Pour Claude Code ou Codex, approuvez la connexion avec
votre compte Claude ou ChatGPT.

Sur une installation native, l’instance par défaut utilise la connexion de
l’utilisateur qui exécute le moteur. Les connexions faites via Olivares restent
des instances distinctes. Les outils indiquent le compte, le forfait, les modèles
et les fenêtres d’utilisation qu’ils exposent. Une valeur indisponible n’est pas
un solde nul ; si une actualisation échoue, le dernier relevé s’affiche comme
périmé, avec la commande qui a échoué.

Pour utiliser une clé d’API à la place, ouvrez **Outils**, ajoutez le fournisseur
sous **Clés d’API**, puis testez la connexion. Pour un modèle local, ajoutez un
point de terminaison Ollama accessible depuis le moteur ; aucune clé d’API n’est
nécessaire. Voir [Ajouter un fournisseur](/how-to/add-a-provider/). Pour un outil
qui exige un modèle, comme Codex, sélectionnez un modèle renvoyé par le test de
connexion si le fournisseur n’a pas de modèle par défaut enregistré. Claude Code
peut utiliser son modèle par défaut natif.

## 3. Démarrer une conversation dans votre projet

1. Ouvrez **Sessions** (`/sessions`) et choisissez **Nouvelle session**.
2. Choisissez un outil prêt. Le formulaire affiche un dossier et le remplit pour
   vous. Laissez-le tel quel pour une session dans son propre dossier, ou choisissez
   **Modifier le dossier** et saisissez `/project` pour utiliser le dossier hôte
   monté par l’installation décrite dans le README.
3. Laissez **Premier message** vide et appuyez sur **Démarrer**. Vous pouvez saisir
   votre message dans la conversation une fois celle-ci ouverte. Le moteur renseigne
   le profil et les chemins de répertoire personnel ; vous n’avez pas besoin de les
   enregistrer pour cette première session.

L’utilisateur du conteneur, UID 65532, a besoin d’un accès en écriture au projet
hôte. Les [instructions sur le dossier de projet](https://github.com/olivaresai/olivares/blob/main/deploy/compose/README.md#work-on-a-host-project-folder)
expliquent les autorisations. Une session peut modifier le contenu du dossier monté ;
utilisez un répertoire de projet, et non l’ensemble de votre répertoire personnel.

Demandez à l’outil d’expliquer le projet. Lisez sa réponse dans la conversation.
Si **Démarrer** ne peut pas aboutir, le formulaire indique ce qui manque. Suivez
cette indication dans **Outils**, puis revenez à la session.

## 4. Approuver une action

Pour essayer une approbation, choisissez **Plus d’options → Demander avant chaque
action** au démarrage de la session. Demandez à l’outil d’exécuter une commande
inoffensive dans le projet. Lorsqu’il demande l’autorisation, ouvrez **Règles**. La
demande qui attend votre décision est marquée en orange et ouverte à côté de la file
d’attente, avec la commande, ce qu’elle atteint et la règle qui l’a déclenchée.
Choisissez **Approuver une fois** (ou appuyez sur `A`), ou **Rejeter** (`D`) puis
ajoutez une note. Revenez à la conversation pour lire le résultat. Une politique de
l’organisation peut aussi exiger une approbation avant qu’une session ne démarre ;
un approbateur doit d’abord accepter cette demande.

## 5. Arrêter et reprendre

Choisissez **Arrêter** sur la session et confirmez si cela vous est demandé. Le
processus de l’outil se termine ; la conversation reste. Choisissez **Reprendre**
sur cette même session et envoyez un autre message. La reprise conserve le profil,
le compte et le dossier sélectionnés, et vérifie qu’ils sont toujours utilisables.

Pour l’exploitation au quotidien, la CLI et les réglages facultatifs de profil, voir
[Exploiter une session fournisseur](/how-to/operate-provider-sessions/).

## Messages de démarrage natif

Les autres méthodes d’installation ont des limites de qualification distinctes ;
voir [Installation](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#installation-qualification).
Un démarrage rapide natif affiche l’adresse de la console et l’étape suivante selon
l’état de sa configuration. Voici les messages pour une première installation, un
retour et une configuration en attente :

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

Lorsqu’aucun identifiant d’inférence à l’échelle de l’hôte n’est configuré, le
journal de démarrage du moteur contient :

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

Cela limite les profils Claude qui utilisent l’injection gérée sans fournisseur lié.
Une clé de fournisseur liée ou la connexion autorisée de l’outil est une source
d’identifiants distincte ; voir
[Exploiter une session fournisseur](/how-to/operate-provider-sessions/#choose-how-the-tool-connects).
