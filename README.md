# systeminfo

Petit serveur web en Go qui expose les informations système de la machine (CPU,
mémoire vive, disque, hôte) via une **API REST** et les affiche dans une
**interface web moderne au thème sombre**.

L'interface HTML est **embarquée dans le binaire** grâce à `//go:embed` : le
binaire compilé est autonome, aucun fichier externe n'est nécessaire pour le
déployer.

## Aperçu

- 🖥️ Tableau de bord temps réel avec 4 cartes : Processeur, Mémoire vive, Disque, Hôte
- 📊 Jauges colorées selon le niveau d'utilisation (vert < 70 %, orange ≥ 70 %, rouge ≥ 90 %)
- 🔄 Rafraîchissement automatique toutes les 3 secondes
- 🟢 Indicateur de connexion à l'API
- 📦 Tout est embarqué dans un seul binaire

## Prérequis

- [Go](https://go.dev/) 1.26 ou supérieur

## Installation et lancement

```bash
# Récupérer les dépendances
go mod tidy

# Lancer directement
go run .

# …ou compiler un binaire autonome
go build -o systeminfo .
./systeminfo
```

Le serveur démarre sur le port `8080`. Ouvrez ensuite votre navigateur sur :

> http://localhost:8080

Le binaire compilé contient l'interface HTML : vous pouvez le copier seul sur
une autre machine et l'exécuter sans dépendances supplémentaires.

### Options de ligne de commande

| Option   | Description                                              | Défaut |
| -------- | ------------------------------------------------------- | ------ |
| `-p`     | Port d'écoute du serveur HTTP                            | `8080` |
| `-r`     | Intervalle de rafraîchissement de l'interface (durée Go) | `3s`   |
| `-h`     | Affiche l'aide                                          |        |

> L'option `-r` accepte une durée au format Go : `500ms`, `5s`, `30s`, `1m`, etc.
> Elle pilote la fréquence d'actualisation de l'interface web (le serveur expose
> cette valeur via l'endpoint `/api/config`).

```bash
# Port personnalisé
./systeminfo -p 9090

# Rafraîchissement toutes les 30 secondes
./systeminfo -r 30s

# Les deux combinés
go run . -p 3000 -r 10s
```

## API REST

### `GET /api/system`

Renvoie l'ensemble des informations système au format JSON.

**Exemple de requête :**

```bash
curl http://localhost:8080/api/system
```

**Exemple de réponse :**

```json
{
  "timestamp": "2026-06-21T22:11:50.700304+02:00",
  "host": {
    "hostname": "macbookair",
    "os": "darwin",
    "platform": "darwin",
    "kernel_arch": "arm64",
    "uptime_seconds": 38029,
    "go_version": "go1.26.4"
  },
  "cpu": {
    "used_percent": 19.45,
    "cores": 8,
    "model_name": "Apple M3"
  },
  "memory": {
    "used_percent": 82.97,
    "used_gb": 7.12,
    "free_gb": 1.46,
    "total_gb": 8.58
  },
  "disk": {
    "used_percent": 67.05,
    "used_gb": 164.35,
    "total_gb": 245.10,
    "path": "/"
  }
}
```

> Note : l'appel mesure l'utilisation CPU sur un court intervalle (500 ms), la
> réponse est donc légèrement différée.

## Structure du projet

```
systeminfo/
├── main.go            # Serveur HTTP, API REST et collecte des métriques
├── public/
│   └── index.html     # Interface web (thème sombre), embarquée via //go:embed
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

## Routes HTTP

| Méthode | Chemin         | Description                                          |
| ------- | -------------- | ---------------------------------------------------- |
| `GET`   | `/`            | Interface web (HTML/CSS/JS embarqué)                 |
| `GET`   | `/api/system`  | Informations système au format JSON                  |
| `GET`   | `/api/config`  | Configuration de l'interface (intervalle en ms)      |

## Dépendances

- [`github.com/shirou/gopsutil/v4`](https://github.com/shirou/gopsutil) —
  collecte multiplateforme des métriques système (CPU, mémoire, disque, hôte).

## Personnalisation

- **Port** : via l'option `-p` au lancement (voir [Options de ligne de commande](#options-de-ligne-de-commande)).
- **Intervalle de rafraîchissement** : via l'option `-r` au lancement (durée Go, ex. `30s`).
- **Partition disque** : par défaut `/`, modifiable dans `collectSystemInfo()`.
