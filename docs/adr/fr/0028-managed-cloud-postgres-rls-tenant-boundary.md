> Traduction automatique. La version anglaise fait foi.

# ADR-0028: Base de données du cloud géré — PostgreSQL géré, avec la sécurité au niveau des lignes comme frontière entre tenants

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0005 (SQLite by default, PostgreSQL at scale), ADR-0027
  (managed-cloud ingress), ADR-0029 (managed-cloud regions), ADR-0022 (source-scoping
  subject axes); the platform decision record for the managed cloud; PostgreSQL
  documentation on row security policies and the AWS database guidance on multi-tenant
  isolation with row-level security, consulted 2026-08-02:
  `https://aws.amazon.com/blogs/database/multi-tenant-data-isolation-with-postgresql-row-level-security/`.

## Contexte et énoncé du problème

L'ADR-0005 a déjà retenu PostgreSQL pour le produit à grande échelle, et le produit dispose
déjà du mécanisme de sécurité au niveau des lignes pour le scoping des tenants. Le cloud géré
n'a pas besoin d'un nouveau modèle de données ; il lui faut une décision sur **qui exploite
la base de données** et sur **ce sur quoi l'on s'appuie précisément pour empêcher qu'un tenant
n'accède aux lignes d'un autre**.

La seconde partie importe davantage que la première. « Nous utilisons la sécurité au niveau
des lignes » n'est pas une propriété tant que les rôles ne sont pas organisés de sorte que les
politiques s'appliquent effectivement. PostgreSQL exclut des politiques des tables deux
catégories d'appelants : les superutilisateurs et les rôles dotés de l'attribut `BYPASSRLS` —
et, par défaut, **le propriétaire d'une table contourne la RLS et n'est soumis à aucune des
politiques de cette table**, sauf si la table est modifiée avec `FORCE ROW LEVEL SECURITY`.
Une application qui se connecte avec le rôle ayant créé le schéma ne bénéficie donc d'*aucune*
isolation entre tenants, tout en donnant l'impression d'en bénéficier. C'est l'erreur la plus
coûteuse que permet cette conception, et elle est silencieuse.

## Facteurs de décision

- L'isolation entre tenants doit être imposée **par la base de données**, et non dépendre de
  la diligence de chaque future requête.
- L'opérateur unique ne doit pas exploiter PostgreSQL : les correctifs, le failover et la
  restauration à un instant donné sont précisément les tâches que l'offre gérée vise à
  supprimer.
- La reprise doit être une propriété de la plateforme, et non d'un runbook dont
  quelqu'un doit penser à lancer l'exécution.
- Toute affirmation concernant l'isolation doit être **testable depuis l'extérieur de
  l'application**.

## Options envisagées

- **A — PostgreSQL autogéré sur des machines virtuelles.** Contrôle total, coût unitaire le
  plus faible, et chaque mise à niveau, exercice de failover et vérification de sauvegarde
  devient notre responsabilité.
- **B — le service PostgreSQL géré du fournisseur cloud, multi-AZ**, avec sauvegardes
  automatisées et restauration à un instant donné.
- **C — le service de cluster compatible PostgreSQL du fournisseur** (architecture de
  stockage partagé, facturation des E/S par requête dans la configuration standard).
- **D — une plateforme PostgreSQL tierce** accessible depuis la même région.

## Résultat de la décision

Option retenue : **B — PostgreSQL géré, multi-AZ**, avec la sécurité au niveau des lignes
comme frontière entre tenants et l'organisation des rôles ci-dessous considérée comme partie
intégrante de la décision, et non comme un détail d'implémentation.

L'organisation des rôles est normative :

1. L'application se connecte avec un rôle qui **n'est pas propriétaire** des tables scopées
   par tenant et **ne détient pas `BYPASSRLS`**.
2. Chaque table scopée par tenant porte **`FORCE ROW LEVEL SECURITY`**, de sorte que le seul
   fait d'en être propriétaire ne permette pas de contourner une politique — cela protège
   contre une future migration qui modifierait le propriétaire d'une table.
3. Le rôle administratif utilisé pour les migrations n'est pas celui qui figure dans la
   chaîne de connexion de l'application.
4. **Périmètre, explicité pour ne jamais être présumé :** le présent document régit le
   **plan de données des tenants** — le schéma contenant les lignes appartenant aux
   tenants, où le moteur émet déjà `ENABLE ROW LEVEL SECURITY`,
   `FORCE ROW LEVEL SECURITY` et une politique par tenant liée à un paramètre de session.
   Les **métadonnées de contrôle propres au plan géré** (registre des tenants, grand livre
   de facturation, instantanés d'utilisation) se trouvent dans un **schéma distinct avec
   une posture distincte** : elles reposent aujourd'hui sur un scoping au niveau applicatif,
   avec un unique rôle applicatif et aucun SQL exposé aux tenants. Cette posture peut fort
   bien être la bonne réponse pour les métadonnées de contrôle, mais elle est actuellement
   **héritée plutôt que décidée**, et ce n'est pas ce que « nous utilisons la sécurité au
   niveau des lignes » laisse entendre au lecteur. Quiconque construit le plan géré doit
   **consigner par écrit la posture de ce schéma et sa justification** avant que celui-ci
   n'héberge les enregistrements d'un client payant.

### Conséquences

- **Avantage :** les correctifs, le failover multi-AZ, les sauvegardes automatisées et la
  restauration à un instant donné deviennent des propriétés de la plateforme. Le runbook de
  reprise après sinistre livré avec le produit reste l'artefact destiné aux déploiements
  auto-hébergés ; il cesse d'être une tâche opérationnelle quotidienne du plan géré.
- **Avantage :** l'isolation devient testable depuis l'extérieur. Le critère d'acceptation est
  une requête exécutée **avec le rôle de l'application**, qui tente de lire les lignes d'un
  autre tenant et n'en obtient aucune — et non une affirmation dans un document de conception.
- **Inconvénient / compromis :** un plancher mensuel fixe supérieur à celui d'une simple
  machine virtuelle, et des mises à niveau de version du moteur qui arrivent selon le
  calendrier du fournisseur plutôt que le nôtre.
- **Neutre :** le rôle administratif du service géré est un rôle de base de données
  privilégié, **pas** un superutilisateur PostgreSQL — il ne dispose d'aucun accès au système
  d'exploitation et ne peut pas réécrire la configuration d'authentification de l'hôte. Il
  s'agit d'une réduction utile du rayon d'impact, mais ce n'est pas ce qui garantit la sécurité
  au niveau des lignes ; c'est l'organisation des rôles ci-dessus qui la garantit.
- **Explicitement NON vérifié, et à ne pas présumer :** si ce rôle administratif détient
  `BYPASSRLS` sur le moteur en cours d'exécution. Il s'agit d'une vérification par une seule
  requête (`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user;`) sur une instance
  réelle, et elle relève de la phase qui en crée une pour la première fois. Tant qu'elle n'a
  pas été exécutée, aucun document ne doit affirmer que le rôle administratif est soumis aux
  politiques des tenants.

## Pourquoi les alternatives ont été rejetées

- **A (PostgreSQL autogéré)** — rejetée parce qu'elle restitue exactement la charge
  opérationnelle que le plan géré vise à absorber, en la concentrant sur un seul opérateur :
  mises à niveau de version, répétition du failover et vérification d'une sauvegarde qui n'est
  réelle que si quelqu'un effectue régulièrement une restauration à partir de celle-ci. Son
  avantage en matière de coût est réel mais faible en valeur absolue ; l'exposition
  opérationnelle, elle, est bien réelle et loin d'être faible.
- **C (service de cluster compatible PostgreSQL)** — rejetée car prématurée. La charge de
  travail est un petit schéma transactionnel avec un débit d'écriture modeste ; l'architecture
  de stockage partagé résout des problèmes de montée en charge que cette charge de travail ne
  rencontre pas, avec un plancher plus élevé et une facturation des E/S par requête dans la
  configuration standard. Elle reste la voie de mise à niveau naturelle si le débit
  d'écriture la justifie un jour.
- **D (plateforme PostgreSQL tierce)** — rejetée pour le store primaire. Le comportement de la
  sécurité au niveau des lignes, le modèle de superutilisateur et les attributs de rôle
  disponibles varient selon le fournisseur et devraient tous être revérifiés par rapport à la
  propriété d'isolation ci-dessus. Il n'y a aucune raison de prendre un risque propre à un
  fournisseur sur l'unique frontière qui ne doit pas céder.
