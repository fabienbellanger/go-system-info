# go-system-info

Petit serveur web en Go qui expose les informations système de la machine (CPU,
charge, mémoire vive, disque, réseau, hôte) via une **API REST** et un **flux
temps réel (Server-Sent Events)**, et les affiche dans une **interface web
moderne au thème sombre** avec jauges colorées et courbes d'évolution
(sparklines).

L'interface web (HTML, CSS, JS, polices `.woff2` et favicon) est **embarquée
dans le binaire** grâce à `//go:embed` : le binaire compilé est autonome et
**fonctionne entièrement hors-ligne**, aucun appel réseau externe (Google Fonts
inclus) ni fichier externe n'est nécessaire pour le déployer.

Le code est organisé en packages : la collecte des métriques (`internal/sysinfo`)
est séparée du serveur HTTP et du routage (`internal/server`).

## Aperçu

- 🖥️ Tableau de bord avec 5 cartes : Processeur, Mémoire vive, Disque, Réseau, Hôte
- 📊 Jauges colorées selon le niveau d'utilisation (vert < 70 %, orange ≥ 70 %, rouge ≥ 90 %)
- 📈 Sparklines : courbes d'évolution CPU/RAM sur ~2 min (historique côté serveur)
- 🌐 Métriques étendues : charge moyenne (load average) et débit réseau (↑/↓ + totaux)
- 🔌 Push **temps réel** via Server-Sent Events (plus de polling)
- 🟢 État de connexion **visible** : en cas de coupure du flux, badge « Hors ligne » avec compteur, jauges et valeurs estompées ; reconnexion automatique
- 📦 Tout est embarqué dans un seul binaire (interface, polices, favicon)
- ⚡ Échantillonnage CPU/réseau en arrière-plan : réponses API instantanées
- 🛡️ Serveur robuste : timeouts HTTP et arrêt gracieux (SIGINT/SIGTERM)
- 📝 Logs structurés (`log/slog`) avec journalisation des requêtes
- 🩺 Endpoints `/api/health` et `/api/version` pour la supervision
- 🐳 Image Docker `scratch` minuscule + CI GitHub Actions (test, lint, build)

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
> make bench              # benchmarks des fonctions critiques
> make lint               # go fmt + go vet
> make build-all          # binaires Linux, macOS (arm64/amd64) et Windows dans dist/
> make docker-build       # image Docker (scratch)
> make docker-run         # build + lancement du conteneur
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

## Docker

Le `Dockerfile` est **multi-stage** : une étape `golang:1.26-alpine` compile un
binaire statique (`CGO_ENABLED=0`), copié dans une image finale `FROM scratch`.
Comme l'interface web est embarquée et qu'aucun appel réseau externe n'est requis,
l'image ne contient **que le binaire** — quelques Mo.

Le plus simple est de passer par le `Makefile` :

```bash
make docker-build   # construit l'image (version injectée depuis git describe)
make docker-run     # construit puis lance le conteneur (port/intervalle du Makefile)
```

Ou directement avec Docker :

```bash
# Build (la version est injectable au build)
docker build --build-arg VERSION=$(git describe --tags --always --dirty) -t go-system-info .

# Lancement (port 8222 exposé par défaut)
docker run --rm -p 8222:8222 go-system-info

# Avec des options : port et intervalle personnalisés
docker run --rm -p 9090:9090 go-system-info -p 9090 -r 5s
```

> Les arguments passés après le nom de l'image sont transmis au binaire
> (`ENTRYPOINT`), exactement comme en ligne de commande.

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
  "load": {
    "load1": 2.59,
    "load5": 3.39,
    "load15": 3.2
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
  },
  "net": {
    "recv_bytes_per_sec": 1436.0,
    "sent_bytes_per_sec": 3315.0,
    "recv_total_bytes": 594250003,
    "sent_total_bytes": 96421144
  },
  "processes": {
    "top_cpu": [
      {
        "name": "chrome",
        "count": 8,
        "user": "fabien",
        "cpu_percent": 152.4,
        "cpu_percent_system": 19.05,
        "mem_percent": 12.4,
        "mem_bytes": 1064960000,
        "pids": [101, 102, 103],
        "killable": true
      }
    ],
    "top_mem": [
      {
        "name": "chrome",
        "count": 8,
        "user": "fabien",
        "cpu_percent": 152.4,
        "cpu_percent_system": 19.05,
        "mem_percent": 12.4,
        "mem_bytes": 1064960000,
        "pids": [101, 102, 103],
        "killable": true
      }
    ]
  }
}
```

- **`load`** : charge système moyenne sur 1, 5 et 15 minutes (load average). À
  comparer au nombre de cœurs : en dessous il reste de la marge, au-dessus le
  système est surchargé. Ce n'est **pas** un pourcentage CPU (peut dépasser le
  nombre de cœurs et compte aussi l'attente d'I/O).
- **`net`** : activité réseau agrégée sur toutes les interfaces — débit
  instantané (octets/s) calculé en différenciant les compteurs cumulés, et
  volumes totaux reçus/émis depuis le démarrage.
- **`processes`** : les deux classements (`top_cpu`, `top_mem`) des 10 plus gros
  consommateurs. Les processus sont **regroupés par application** : chaque
  processus est rattaché à son ancêtre de plus haut niveau (le processus dont le
  parent est `launchd`/pid 1) et **tout le sous-arbre est sommé** sous le nom de
  cette racine — ainsi tous les « Helium Helper » comptent dans « Helium », et un
  outil lancé depuis un terminal/IDE est comptabilisé sous celui-ci. `count` est
  le nombre de processus de l'arbre, `pids` leur liste. `cpu_percent` est exprimé
  en **% d'un cœur** (façon `top`/`htop`) : un programme multi-thread peut
  dépasser 100 %. `cpu_percent_system` est la **même charge rapportée à la
  machine entière** (`cpu_percent` / nombre de cœurs) : sur la même base que la
  jauge CPU globale (0–100 %), la somme des processus s'en approche. `mem_bytes`
  est le RSS cumulé. `user` est le propriétaire de la
  racine, et `killable` vaut `true` lorsque **tout le sous-arbre** appartient à
  l'utilisateur ayant lancé le serveur — seuls ces arbres peuvent être terminés
  (voir `POST /api/processes/kill`). Champ **omis** d'un relevé ponctuel obtenu
  via la fonction `Collect` libre (sans collecteur d'arrière-plan).

> Note : l'utilisation CPU, le débit réseau et le classement des processus (relevé
> plus espacé, toutes les 3 s, car énumérer tous les processus est coûteux) sont
> échantillonnés en continu par des goroutines d'arrière-plan et mis en cache. Les
> requêtes `GET /api/system` sont donc instantanées (aucun délai de mesure côté
> requête). L'utilisation CPU des processus est, comme celle du système, calculée
> en différenciant les temps CPU cumulés entre deux relevés. L'utilisation CPU
> est calculée en différenciant les temps CPU cumulés ; un relevé où les
> compteurs n'ont pas progressé (cas observé sur macOS) est ignoré pour éviter un
> `0 %` parasite sous charge — la dernière valeur connue est alors conservée.

### `GET /api/stream` (temps réel, SSE)

Flux **Server-Sent Events** : c'est le canal qu'utilise l'interface web à la
place du polling. Le serveur émet un premier événement immédiatement, puis un
nouveau à chaque intervalle de rafraîchissement (option `-r`). Chaque événement
pousse un état combiné `{ "system": {…}, "history": [...] }`, ce qui remplace en
une seule connexion les appels répétés à `/api/system` et `/api/history`.

```bash
curl -N http://localhost:8222/api/stream
# data: {"system":{…},"history":[{"cpu":19.4,"mem":83.0}, …]}
#
# data: {"system":{…},"history":[ … ]}
```

> La connexion reste ouverte : le `WriteTimeout` du serveur est neutralisé pour
> cette requête uniquement. En cas de coupure réseau, le navigateur
> (`EventSource`) se reconnecte automatiquement. L'interface signale la coupure
> visuellement (badge « Hors ligne » avec décompte, jauges et valeurs estompées)
> après un court délai de grâce (~2 intervalles), pour ignorer les micro-coupures.

### `GET /api/history`

Renvoie l'historique glissant des mesures CPU/mémoire (anneau circulaire conservé
côté serveur, ~120 points à 1 point/s, soit environ 2 minutes), du plus ancien au
plus récent. C'est la source des sparklines de l'interface.

```bash
curl http://localhost:8222/api/history
# [{"cpu":19.4,"mem":83.0},{"cpu":21.1,"mem":82.7}, …]
```

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

### `POST /api/processes/kill`

Termine (signal **SIGTERM**) un ou plusieurs processus, identifiés par leurs PID.
C'est le pendant écriture de la carte « Processus » de l'interface : le bouton de
terminaison envoie les `pids` du groupe fusionné.

```bash
curl -X POST http://localhost:8222/api/processes/kill \
  -H 'Content-Type: application/json' \
  -d '{"pids":[101,102,103]}'
# {"results":[{"pid":101,"ok":true},{"pid":102,"ok":true},{"pid":103,"ok":true}]}
```

> **Garde-fou de sécurité** : le serveur ne termine que les processus appartenant
> à **l'utilisateur qui l'a lancé**. Toute autre cible est refusée (`ok:false`
> avec un message), avant même l'envoi du signal. Chaque PID est revérifié au
> moment de la terminaison (et non d'après l'instantané mis en cache).
>
> ⚠️ Cet endpoint a un effet de bord destructif. Si vous exposez le serveur
> au-delà de `localhost`, protégez-le en amont (pare-feu, reverse-proxy
> authentifié) : la restriction « même utilisateur » limite la portée mais
> n'authentifie pas l'appelant.

### `GET /api/processes/detail`

Renvoie le détail courant des processus dont les PID sont passés dans le
paramètre `pids` (séparés par des virgules). C'est ce qui alimente le panneau de
détails de l'interface lorsqu'on sélectionne une application : `ppid` et `name`
permettent d'y reconstruire l'**arbre** parent → enfants.

```bash
curl 'http://localhost:8222/api/processes/detail?pids=101,102'
# {"instances":[{"pid":101,"ppid":1,"user":"fabien","name":"Helium",
#   "cmdline":"/Applications/…","status":"running","threads":42,
#   "create_time":1719400000000,"mem_bytes":123456}]}
```

Les processus inaccessibles ou disparus sont simplement absents de la réponse
(le nombre de PID interrogeables en une requête est borné).

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

| Flag       | Effet                                                                              |
| ---------- | ---------------------------------------------------------------------------------- |
| `--tags`   | Utilise le tag git le plus récent comme base (ex. `v1.2.0`)                        |
| `--always` | Si aucun tag n'existe, retombe sur le hash court du commit (ex. `131ee4b`)         |
| `--dirty`  | Ajoute le suffixe `-dirty` si l'arbre de travail a des modifications non commitées |

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
├── main_test.go               # Tests du parsing des flags
├── internal/
│   ├── sysinfo/
│   │   ├── sysinfo.go         # Collecte des métriques (CPU, charge, mémoire, disque, réseau, hôte)
│   │   └── sysinfo_test.go    # Tests + benchmarks du package sysinfo
│   └── server/
│       ├── server.go          # Serveur HTTP, routage, API REST et flux SSE
│       └── server_test.go     # Tests du package server
├── public/                    # Interface web embarquée via //go:embed
│   ├── index.html             # Structure de la page (thème sombre)
│   ├── styles.css             # Styles + polices @font-face locales
│   ├── app.js                 # Sparklines, flux SSE et rendu de l'interface
│   ├── favicon.svg            # Favicon (écran stylisé)
│   └── fonts/                 # Polices .woff2 embarquées (Inter, JetBrains Mono)
├── .github/workflows/ci.yml   # CI : tests, lint, build multi-plateforme
├── Dockerfile                 # Build multi-stage → image scratch
├── .dockerignore
├── .golangci.yml              # Configuration golangci-lint
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

## Routes HTTP

| Méthode | Chemin         | Description                                     |
| ------- | -------------- | ----------------------------------------------- |
| `GET`   | `/`            | Interface web (HTML/CSS/JS embarqué)            |
| `GET`   | `/api/system`  | Informations système au format JSON             |
| `GET`   | `/api/stream`  | Flux temps réel (SSE) : système + historique    |
| `GET`   | `/api/history` | Historique CPU/mémoire (sparklines)             |
| `GET`   | `/api/config`  | Configuration de l'interface (intervalle en ms) |
| `GET`   | `/api/health`  | Sonde de santé (`{"status":"ok"}`)              |
| `GET`   | `/api/version` | Version du binaire injectée au build            |
| `POST`  | `/api/processes/kill` | Termine des processus (SIGTERM, même utilisateur uniquement) |
| `GET`   | `/api/processes/detail` | Détail par PID (`?pids=…`) du processus sélectionné       |

## Qualité, tests et CI

- **Tests** : `make test` (avec détecteur de data races). Les handlers sont
  testables sans dépendre de la machine grâce à une interface `systemCollector`
  injectable, et le parsing des flags est isolé dans `parseFlags`.
- **Benchmarks** : `make bench` mesure les fonctions critiques — l'assemblage
  d'un `Info` (coût réel d'une requête) et la copie de l'historique (par
  événement SSE).
- **Lint** : `make lint` (go fmt + go vet) en local ; en CI, `golangci-lint`
  avec la config `.golangci.yml` (jeu `standard` + `bodyclose`/`unconvert`,
  formateurs `gofmt`/`goimports`).
- **CI GitHub Actions** (`.github/workflows/ci.yml`) : à chaque push sur `main`
  et chaque pull request — trois jobs **test** (`go vet` + `go test -race`),
  **lint** (`golangci-lint`) et **build** (`make build-all`, cross-compilation
  des 4 plateformes).

## Dépendances

- [`github.com/shirou/gopsutil/v4`](https://github.com/shirou/gopsutil) —
  collecte multiplateforme des métriques système (CPU, charge, mémoire, disque,
  réseau, hôte).

## Personnalisation

- **Port** : via l'option `-p` au lancement (voir [Options de ligne de commande](#options-de-ligne-de-commande)).
- **Intervalle de rafraîchissement** : via l'option `-r` au lancement (durée Go, ex. `30s`).
- **Partition disque** : par défaut `/`, modifiable dans `Collect()` (`internal/sysinfo/sysinfo.go`).
