> Traduction automatique. La version anglaise fait foi.

# ADR-0029: Régions du cloud géré — une région primaire, la résidence des données étant assurée par l'auto-hébergement

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0027 (managed-cloud ingress), ADR-0028 (managed-cloud database),
  ADR-0020 (enterprise private-repo distribution), ADR-0024 (DDIL offline semantics and
  signed bundles); the platform decision record for the managed cloud.

## Contexte et énoncé du problème

Deux questions doivent être traitées ensemble, car une mauvaise réponse à l'une impose une
mauvaise réponse à l'autre : **où le plan géré s'exécute-t-il**, et **que dit-on à un client
qui demande où résident ses données**.

La tentation est de choisir la région qui facilite la seconde réponse — une région dont la
juridiction présente bien dans une section sur la conformité — et d'accepter la latence que
cela implique pour les clients réels. C'est le mauvais ordre. Cela repose également sur une
idée fausse qu'il convient de consigner une fois, à un endroit durable, afin que personne ne la
redéduise : **le lieu où les octets sont stockés ne détermine pas quelle législation sur la
protection des données s'applique.** Servir des personnes concernées dans une juridiction rend
la législation de cette juridiction applicable, quel que soit le lieu d'hébergement.

## Facteurs de décision

- La latence pour les clients auxquels le produit est réellement vendu.
- Les preuves de conformité demandées par un acheteur enterprise, qui concernent en grande
  partie le **fournisseur d'infrastructure**, et non la région.
- Ne pas payer le coût fixe d'une seconde région — ni la complexité permanente du traitement
  interrégional des données — avant qu'un client ne l'exige.
- Disposer d'une réponse véridique et sans détour pour un client soumis à une exigence stricte
  de résidence.

## Options envisagées

- **A — une seule région primaire dans le marché cible**, avec une seconde région comme projet
  déclenché par la demande.
- **B — deux régions dès le lancement**, une pour chaque marché majeur.
- **C — une région primaire choisie pour le discours réglementaire** plutôt que pour la
  latence des clients.

## Résultat de la décision

Option retenue : **A — une seule région primaire, située dans le marché cible (Est des
États-Unis)**. Une seconde région est un projet déclenché par une exigence financée, et non un
élément du lancement. L'épinglage de région par tenant et la réplication interrégionale sont
délibérément hors du périmètre de la première version gérée.

Un client soumis à une **exigence contractuelle ou réglementaire de résidence que la région
primaire ne satisfait pas** est servi par l'**édition auto-hébergée** — qui constitue la forme
principale du produit, s'exécute dans l'infrastructure du client et répond entièrement, plutôt
que partiellement, à la question de la résidence. Ce n'est pas un contournement ; c'est la
réponse la plus forte, et elle est disponible dès le premier jour.

### Conséquences

- **Avantage :** le déploiement comprend une région, une base de données et un domaine de
  défaillance sur lesquels raisonner, et le budget de latence est dépensé là où se trouvent les
  clients.
- **Avantage :** la réponse concernant la résidence est honnête et immédiate —
  auto-hébergement — plutôt qu'une promesse de roadmap.
- **Inconvénient / compromis :** un client qui veut à la fois une offre *gérée* **et** une
  résidence hors des États-Unis ne peut pas être servi tant qu'une seconde région n'existe pas.
  Il s'agit d'une lacune connue et acceptée, qui doit être énoncée clairement dans les supports
  commerciaux plutôt que dissimulée.
- **Inconvénient :** une région unique constitue un domaine de défaillance régional unique. Le
  multi-AZ (ADR-0028) couvre la perte d'une zone de disponibilité, **pas** celle d'une région.
  Pour une panne régionale, la reprise consiste à restaurer ailleurs depuis les sauvegardes,
  avec un temps de reprise mesuré en heures, et ce scénario doit faire l'objet d'un
  **exercice** avant d'être annoncé à qui que ce soit.
- **Neutre, et c'est la raison de consigner ce point :** choisir une région primaire aux
  États-Unis signifie que les données personnelles de personnes concernées hors des États-Unis
  sont **transférées**, ce qui requiert un mécanisme de transfert valide et un accord de
  traitement désignant le fournisseur d'infrastructure comme sous-traitant ultérieur. Le
  présent ADR ne crée ni l'un ni l'autre. Il consigne que **le choix de la région ne supprime
  pas l'obligation** — afin qu'aucun lecteur futur ne prenne « nous hébergeons dans la région
  X » pour une réponse en matière de conformité. Il s'agit d'un document d'ingénierie, et non
  d'un conseil juridique ; les instruments eux-mêmes relèvent du volet conformité.

## Pourquoi les alternatives ont été rejetées

- **B (deux régions au lancement)** — rejetée car elle revient à payer deux fois, de manière
  permanente, pour un client qui n'existe pas encore. Une seconde région double le plancher
  fixe d'infrastructure et ajoute une catégorie de problèmes qui ne disparaît jamais : quelle
  région est propriétaire d'un tenant, ce qui transite entre elles et comment prouver une
  affirmation de résidence par tenant plutôt que par plateforme. Tout cela mérite d'être fait
  lorsqu'une exigence signée le finance.
- **C (région choisie pour le discours réglementaire)** — rejetée parce qu'elle achète un
  paragraphe et le paie à chaque requête. Elle ne fournit pas non plus ce qu'elle semble
  fournir : comme indiqué ci-dessus, le lieu d'hébergement ne détermine pas la législation
  applicable ; le discours serait donc moins solide qu'il n'y paraît, tandis que le coût de
  latence serait exactement aussi important qu'il en a l'air.
