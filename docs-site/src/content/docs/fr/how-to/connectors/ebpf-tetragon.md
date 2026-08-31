---
title: "eBPF / Tetragon (le filet de sécurité noyau)"
description: >-
  Câblez la moitié non coopérative de la carte d'accès : Tetragon capture les
  événements fichier et réseau du noyau hors du contrôle de l'agent, et le
  connecteur transforme son export JSON en arêtes d'accès honnêtement
  approximatives — plus un détecteur anti-évasion optionnel.
sidebar:
  order: 3
---

La source `ebpf` est la **moitié anti-évasion** de la carte R/RW. Là où le
chemin coopératif voit ce qu'un agent *rapporte*, celle-ci voit ce que le noyau
*a fait* — lectures/écritures de fichiers et connexions sortantes — même quand
un agent désactive sa propre télémétrie, parce qu'elle tourne **hors du contrôle
de l'agent**.

Deux décisions de conception la définissent, et toutes deux constituent la
posture de sécurité :

- **Elle ne charge pas elle-même de programmes eBPF.** [Tetragon](https://tetragon.io)
  effectue la capture noyau, déployé comme service durci distinct détenant
  `CAP_BPF` + `CAP_PERFMON`. Le connecteur est un **consommateur en lecture
  seule, sans aucune capacité**, de l'export d'événements JSON de Tetragon (un
  fichier/FIFO partagé, mode `0600`, ou stdin).
- **Elle est aveugle aux corps TLS et aux charges utiles.** Elle observe des
  relations d'accès — jamais le contenu.

Le dépôt fournit le déploiement de référence sous `connectors/ebpf/deploy/` :
un DaemonSet Tetragon durci, les deux TracingPolicies (accès fichier, réseau),
et une variante Compose pour les hôtes uniques.

## Ce qu'elle émet

| Champ | Valeur |
|---|---|
| Source du signal | `ebpf` |
| Mode | `read` / `write` fichier, arêtes de connexion réseau |
| Origine | une **identité d'exécution** (processus/conteneur) — type `identity`, jamais un agent résolu |
| Confiance | **toujours `approximate`** — voir ci-dessous |
| Palier de couverture | filet de sécurité noyau |

L'`approximate` est précis, pas modeste : l'*accès* est une vérité terrain du
noyau (l'appel système a eu lieu) ; ce que le noyau ne peut pas fournir, c'est
l'*agent* — il connaît le processus et le cgroup, pas quel agent gouverné
c'était. Le module de carte d'accès améliore l'attribution lorsqu'une source
d'identité lie l'identité d'exécution à un agent.

## 1. Déployer Tetragon (le capteur)

Sur Kubernetes, appliquez le DaemonSet et les TracingPolicies fournis :

```bash
kubectl apply -f connectors/ebpf/deploy/tetragon-daemonset.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-file-access.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-network.yaml
```

Tetragon écrit son export JSON dans le volume partagé
(`/var/run/olivares/tetragon.log`) ; le connecteur le lit de l'autre côté. Sur
un hôte unique, `connectors/ebpf/deploy/docker-compose.yaml` est la même
séparation sans Kubernetes. L'architecture complète et les notes de durcissement
sont dans `connectors/ebpf/deploy/README.md`.

## 2. Déclarer la source

```json
{
  "sources": [{
    "name": "node-kernel-backstop",
    "kind": "ebpf",
    "tenant": "<tenant-id>",
    "config": {
      "events_path": "/var/run/olivares/tetragon.log",
      "detect_evasion": "true"
    }
  }]
}
```

| Clé | Défaut | Signification |
|---|---|---|
| `events_path` | `-` (stdin) | flux d'événements JSON de Tetragon — fichier, FIFO ou stdin |
| `follow` | `true` | continuer la lecture à mesure que le flux grandit |
| `detect_evasion` | `false` | optionnel : signaler un processus d'agent connu dont la télémétrie coopérative devient muette alors que le noyau le voit encore agir |
| `evasion_window` | `5m` | délai de grâce avant de signaler une connexion coopérative manquante |
| `agent_signatures` | `claude,claude-code` | noms d'exécutables classés comme agents coopératifs pour le détecteur |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | les points d'accès de télémétrie coopérative dont le détecteur corrèle les connexions |

Le connecteur consomme les événements Tetragon `ProcessKprobe` (opérations sur
fichiers et connexions réseau) et `ProcessExit` (état du détecteur) ;
`ProcessExec` sert au contexte d'attribution et n'est jamais émis comme arête.

## 3. Ce que vous verrez dans la console

Les arêtes du noyau rejoignent la carte d'accès attribuées à des identités
d'exécution, toujours marquées `approximate`. La sortie du détecteur atterrit
dans **Security** sous forme de constats — une session qui cesse d'émettre alors
que le noyau voit encore de l'activité est exactement le cas pour lequel cette
source existe :

<img class="light:sl-hidden" src="/console/security-dark.png" alt="La vue Security listant les constats issus des sources détectives du parc." />
<img class="dark:sl-hidden" src="/console/security-light.png" alt="La vue Security listant les constats issus des sources détectives du parc." />

## Limites assumées

- **Sa profondeur d'attribution de bout en bout est encore en cours de
  validation.** Le chemin coopératif et l'audit natif des magasins sont les
  signaux vérifiés et à haute fidélité ; traitez le filet de sécurité noyau
  comme un rehausseur de plancher, pas comme une source primaire achevée
  ([Honnêteté et limites](/fr/start/honesty-and-limits/)).
- **La portée de Tetragon est celle de ses TracingPolicies.** Les politiques
  fournies couvrent l'accès fichier et les connexions réseau ; ce qu'elles ne
  tracent pas n'existe pas dans l'export.
- **Processus ≠ agent.** Sans liaison d'identité, chaque arête du noyau reste
  `approximate` — par conception, pas par accident.

## Voir aussi

- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — la moitié coopérative
  que ceci protège.
- [SSO/SCIM et sources d'identité](/fr/how-to/connectors/sso-scim-identity/) —
  comment l'attribution est améliorée.
- [Durcissement de sécurité](/fr/how-to/security-hardening/) — où le filet de
  sécurité s'insère dans la posture défensive.
