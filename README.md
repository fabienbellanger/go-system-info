# go-system-info

Petit serveur web en Go qui expose les informations système de la machine (CPU,
mémoire vive, disque, hôte) via une **API REST** et les affiche dans une
**interface web moderne au thème sombre**.

L'interface web (HTML, CSS, JS, polices `.woff2` et favicon) est **embarquée
dans le binaire** grâce à `//go:embed` : le binaire compilé est autonome et
**fonctionne entièrement hors-ligne**, aucun appel réseau externe (Google Fonts
inclus) ni fichier externe n'est nécessaire pour le déployer.

Le code est organisé en packages : la collecte des métriques (`internal/sysinfo`)
est séparée du serveur HTTP et du routage (`internal/server`).

## Aperçu

- 🖥️ Tableau de bord temps réel avec 4 cartes : Processeur, Mémoire vive, Disque, Hôte
- 📊 Jauges colorées selon le niveau d'utilisation (vert < 70 %, orange ≥ 70 %, rouge ≥ 90 %)
- 🔄 Rafraîchissement automatique toutes les 3 secondes
- 🟢 Indicateur de connexion à l'API
- 📦 Tout est embarqué dans un seul binaire
- ⚡ Échantillonnage CPU en arrière-plan : réponses API instantanées
- 🛡️ Serveur robuste : timeouts HTTP et arrêt gracieux (SIGINT/SIGTERM)
- 📝 Logs structurés (`log/slog`) avec journalisation des requêtes
- 🩺 Endpoints `/api/health` et `/api/version` pour la supervision

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

Le serveur démarre sur le port `8222`. Ouvrez ensuite votre navigateur sur :

> http://localhost:8222

Le binaire compilé contient l'interface web : vous pouvez le copier seul sur
une autre machine et l'exécuter sans dépendances supplémentaires.

> Les cibles `make build-*` injectent automatiquement la version (issue de
> `git describe`) dans le binaire via `-ldflags "-X main.version=..."`. Sans
> injection (ex. `go run .`), la version vaut `dev`. À l'arrêt, le serveur
> attend la fin des requêtes en cours (arrêt gracieux) sur réception de
> `SIGINT`/`SIGTERM`.

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

| Option | Description                                              | Défaut |
| ------ | -------------------------------------------------------- | ------ |
| `-p`   | Port d'écoute du serveur HTTP                            | `8222` |
| `-r`   | Intervalle de rafraîchissement de l'interface (durée Go) | `3s`   |
| `-h`   | Affiche l'aide                                           |        |

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
curl http://localhost:8222/api/system
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
    "total_gb": 245.1,
    "path": "/"
  }
}
```

> Note : l'utilisation CPU est échantillonnée en continu par une goroutine
> d'arrière-plan et mise en cache. Les requêtes `GET /api/system` sont donc
> instantanées (aucun délai de mesure côté requête).

### `GET /api/health`

Sonde de santé pour les orchestrateurs / health checks.

```bash
curl http://localhost:8222/api/health
# {"status":"ok"}
```

### `GET /api/version`

Renvoie la version du binaire, injectée au build via `-ldflags`.

```bash
curl http://localhost:8222/api/version
# {"version":"v1.0.0"}
```

## Versionnage du binaire

La version exposée par `/api/version` (et journalisée au démarrage) est
**injectée au moment du build**, sans avoir à modifier le code source. Deux
lignes du `Makefile` s'en chargent :

```makefile
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
```

### Étape 1 — Calculer la version

`$(shell ...)` exécute une commande shell **au moment où make lit le Makefile**
et capture sa sortie dans la variable `VERSION`. La commande utilisée est
`git describe --tags --always --dirty` :

| Flag        | Effet                                                                   |
| ----------- | ----------------------------------------------------------------------- |
| `--tags`    | Utilise le tag git le plus récent comme base (ex. `v1.2.0`)             |
| `--always`  | Si aucun tag n'existe, retombe sur le hash court du commit (ex. `131ee4b`) |
| `--dirty`   | Ajoute le suffixe `-dirty` si l'arbre de travail a des modifications non commitées |

Exemples de sorties possibles :

- `v1.2.0` → on est exactement sur le tag, arbre propre
- `v1.2.0-3-g131ee4b` → 3 commits après le tag `v1.2.0`, sur le commit `131ee4b`
- `131ee4b-dirty` → pas de tag, et des changements non commités

Le `2>/dev/null || echo dev` est un **filet de sécurité** : si git n'est pas
installé ou qu'on n'est pas dans un dépôt git, la commande échoue, l'erreur est
masquée (`2>/dev/null`) et le `||` fait retomber sur la valeur littérale `dev`.

### Étape 2 — Injecter dans le binaire

`-X importpath.name=value` est un flag de l'éditeur de liens Go
(`go tool link`). Il **écrit une valeur dans une variable string au moment de
l'édition de liens**, sans recompiler le code. Ici il cible la variable déclarée
dans `main.go` :

```go
// version est la version du binaire, injectée au build via
// -ldflags "-X main.version=...". Vaut "dev" pour les exécutions locales.
var version = "dev"
```

Contraintes du flag `-X` :

- La variable doit être une `var` de type `string` au **niveau package** (pas une constante, pas une variable locale).
- Sa valeur initiale (`"dev"`) sert de **défaut** : c'est ce qu'on obtient avec `go run .` ou `make serve`, qui ne passent pas de `-ldflags`.
- `main.version` = package `main`, variable `version`.

> Les flags `-s -w` (déjà présents) sont indépendants : ils suppriment la table
> des symboles et les infos de debug pour alléger le binaire. Le `-X` s'ajoute
> simplement à ces flags d'édition de liens.

### Le chemin complet

```
make build-darwin-arm64
   └─ go build -ldflags="-s -w -X main.version=v1.2.0-3-g131ee4b" ...
         └─ l'éditeur de liens remplace "dev" par "v1.2.0-3-g131ee4b" dans le binaire
               └─ au runtime : cfg := server.Config{Version: version}
                     └─ exposé via GET /api/version  et  loggé au démarrage
```

### Vérifier

```bash
make build-darwin-arm64
./dist/"Go System Info-darwin-arm64" &
curl localhost:8222/api/version   # {"version":"131ee4b-dirty"}
```

Sans injection, le défaut s'applique :

```bash
go run .   # log au démarrage : version=dev
```

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
│   ├── styles.css             # Styles + polices @font-face locales
│   ├── app.js                 # Logique d'actualisation et appels API
│   ├── favicon.svg            # Favicon (écran stylisé)
│   └── fonts/                 # Polices .woff2 embarquées (Inter, JetBrains Mono)
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

## Routes HTTP

| Méthode | Chemin        | Description                                     |
| ------- | ------------- | ----------------------------------------------- |
| `GET`   | `/`            | Interface web (HTML/CSS/JS embarqué)            |
| `GET`   | `/api/system`  | Informations système au format JSON             |
| `GET`   | `/api/config`  | Configuration de l'interface (intervalle en ms) |
| `GET`   | `/api/health`  | Sonde de santé (`{"status":"ok"}`)              |
| `GET`   | `/api/version` | Version du binaire injectée au build            |

## Dépendances

- [`github.com/shirou/gopsutil/v4`](https://github.com/shirou/gopsutil) —
  collecte multiplateforme des métriques système (CPU, mémoire, disque, hôte).

## Personnalisation

- **Port** : via l'option `-p` au lancement (voir [Options de ligne de commande](#options-de-ligne-de-commande)).
- **Intervalle de rafraîchissement** : via l'option `-r` au lancement (durée Go, ex. `30s`).
- **Partition disque** : par défaut `/`, modifiable dans `Collect()` (`internal/sysinfo/sysinfo.go`).
