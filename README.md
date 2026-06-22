# systeminfo

Petit serveur web en Go qui expose les informations système de la machine (CPU,
mémoire vive, disque, hôte) via une **API REST** et les affiche dans une
**interface web moderne au thème sombre**.

L'interface web (HTML, CSS et JS) est **embarquée dans le binaire** grâce à
`//go:embed` : le binaire compilé est autonome, aucun fichier externe n'est
nécessaire pour le déployer.

Le code est organisé en packages : la collecte des métriques (`internal/sysinfo`)
est séparée du serveur HTTP et du routage (`internal/server`).

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

Le binaire compilé contient l'interface web : vous pouvez le copier seul sur
une autre machine et l'exécuter sans dépendances supplémentaires.

> Un `Makefile` regroupe les tâches courantes. Par exemple :
>
> ```bash
> make serve              # go run avec port et intervalle configurables
> make watch              # relance automatique au changement (watchexec)
> make test               # tests avec détecteur de data races
> make test-cover         # tests + rapport de couverture
> make lint               # go fmt + go vet
> make build-all          # binaires Linux, macOS (arm64/amd64) et Windows dans dist/
> ```

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
├── main.go                    # Point d'entrée : flags, //go:embed et démarrage du serveur
├── internal/
│   ├── sysinfo/
│   │   ├── sysinfo.go         # Collecte des métriques (CPU, mémoire, disque, hôte)
│   │   └── sysinfo_test.go    # Tests du package sysinfo
│   └── server/
│       ├── server.go          # Serveur HTTP, routage et API REST
│       └── server_test.go     # Tests du package server
├── public/                    # Interface web embarquée via //go:embed
│   ├── index.html             # Structure de la page (thème sombre)
│   ├── styles.css             # Styles
│   └── app.js                 # Logique d'actualisation et appels API
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
- **Partition disque** : par défaut `/`, modifiable dans `Collect()` (`internal/sysinfo/sysinfo.go`).
