# go-system-info

[![CI](https://github.com/fabienbellanger/go-system-info/actions/workflows/ci.yml/badge.svg)](https://github.com/fabienbellanger/go-system-info/actions/workflows/ci.yml)

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

- 🖥️ Tableau de bord : trois cartes-jauges (Processeur, Mémoire vive, Disque),
  une **bande d'état** compacte (Réseau + Hôte) et la carte **Processus** — un
  clic sur la carte Processeur fait glisser la page jusqu'aux processus
- 📊 Jauges colorées selon le niveau d'utilisation (vert < 70 %, orange ≥ 70 %, rouge ≥ 90 %)
- 📈 Sparklines : courbes d'évolution CPU/RAM sur ~2 min (historique côté serveur)
- 🧮 Grille d'utilisation **par cœur** et **température** (capteur le plus chaud, si disponible)
- 🔔 Titre d'onglet dynamique (CPU/RAM), préfixé d'un ⚠️ au-delà du seuil critique
- 🗂️ **Sélecteur de volume** disque (choix parmi les volumes montés), avec **système de fichiers** et **débit d'E/S** (lecture/écriture)
- 💾 **Swap** (mémoire d'échange) sur la carte Mémoire
- 🔎 Liste de processus avec **recherche** par nom et **tri** (nom / valeur, ↑↓)
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

| Option          | Description                                                       | Défaut                   |
| --------------- | ----------------------------------------------------------------- | ------------------------ |
| `-p`            | Port d'écoute du serveur HTTP (1–65535)                           | `8222`                   |
| `-r`            | Intervalle de rafraîchissement de l'interface (durée Go)          | `3s`                     |
| `-d`            | Chemin du volume à surveiller                                     | `/` (`C:\` sous Windows) |
| `-host`         | Adresse d'écoute (vide = toutes les interfaces)                   | _(toutes)_               |
| `-readonly`     | Mode lecture seule : désactive la terminaison de processus        | `false`                  |
| `-trusted-host` | Noms d'hôte de confiance additionnels (en-tête Host), séparés `,` | _(aucun)_                |
| `-h`            | Affiche l'aide                                                    |                          |

> L'option `-r` accepte une durée au format Go : `500ms`, `5s`, `30s`, `1m`, etc.
> Elle pilote la fréquence d'actualisation de l'interface web (le serveur expose
> cette valeur via l'endpoint `/api/config`). Le minimum accepté est `250ms` ;
> une valeur nulle ou plus courte est refusée au démarrage.

> L'option `-host` restreint l'adresse d'écoute. Par défaut, le serveur écoute
> sur **toutes les interfaces** (accessible depuis le réseau local, pratique pour
> Docker) ; `-host 127.0.0.1` le limite à la **machine locale** — recommandé si
> vous n'avez pas besoin d'un accès distant, l'endpoint de terminaison ayant un
> effet destructeur.

> L'option `-readonly` neutralise la seule action destructrice de l'API
> (`POST /api/processes/kill` répond alors `403`) : l'interface reste
> consultable mais ne peut plus terminer de processus. C'est le réglage
> recommandé dès que le serveur est exposé au-delà de `127.0.0.1` — il rend
> défendable une écoute sur toutes les interfaces. Le front masque
> automatiquement les boutons de terminaison dans ce mode.

### Protection de l'en-tête `Host` (DNS rebinding)

Le serveur **valide l'en-tête `Host`** de chaque requête : seuls `localhost`, le
nom de la machine, les adresses IP littérales (jamais usurpables par rebinding)
et les noms passés via `-trusted-host` sont acceptés ; tout autre nom reçoit un
`403`. Cela ferme l'attaque par **DNS rebinding**, où une page web tierce
re-résout son domaine vers `127.0.0.1` pour dialoguer en « même origine » avec ce
démon local (et contourner ainsi les protections CSRF). Derrière un reverse proxy
exposant l'application sous un nom de domaine, déclarez ce nom :
`-trusted-host monitoring.example.com` (valeurs multiples séparées par des
virgules).

> Par ailleurs, `GET /api/processes/detail` ne renvoie que les processus de
> l'utilisateur ayant lancé le serveur : la ligne de commande d'un processus
> d'autrui (qui peut contenir des secrets) n'est jamais divulguée, au même titre
> que la terminaison est déjà réservée à ses propres processus.

> L'option `-d` choisit le volume dont l'occupation est affichée. Par défaut, la
> racine du système de fichiers (`/`, ou `C:\` sous Windows). Exemple :
> `-d /System/Volumes/Data` sous macOS, `-d D:\` sous Windows.

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

## Lancer en tant que service

Le binaire est autonome (interface embarquée, aucune dépendance réseau) et gère
l'**arrêt gracieux** sur `SIGINT`/`SIGTERM` (`Ctrl+C` sous Windows). Il se prête
donc bien à un lancement automatique au démarrage de la machine, supervisé par le
gestionnaire de services natif de chaque système.

> ⚠️ **Périmètre de la terminaison de processus.** L'endpoint
> `POST /api/processes/kill` ne tue **que les processus de l'utilisateur qui
> exécute le serveur** (garde-fou `killOwnedProcess`). Le choix de l'utilisateur
> sous lequel tourne le service a donc un effet direct :
>
> - Pour piloter **vos propres** applications depuis l'interface, lancez le
>   service **sous votre compte** (LaunchAgent macOS, service utilisateur, etc.).
> - Un service **système** sous un compte dédié (ou `root`/`LocalSystem`) ne
>   verra/terminera que les processus de **ce** compte. Lancer en `root` permet
>   de tout terminer — à n'utiliser qu'en connaissance de cause.

Dans les exemples ci-dessous, on suppose le binaire installé en
`/usr/local/bin/go-system-info` (Unix) ou
`C:\Program Files\go-system-info\go-system-info.exe` (Windows), écoutant sur le
port `8222` avec un rafraîchissement de `3s`. Adaptez chemins, port et compte.

> 💡 **Raccourci (macOS / Linux).** `make install` automatise tout ce qui suit
> pour le système hôte : compilation, copie du binaire et génération + activation
> du fichier de service (LaunchAgent sous macOS, unité systemd sous Linux).
>
> - **macOS** : LaunchAgent **utilisateur**, à lancer **sans `sudo`**. Le binaire
>   va dans `$HOME/.local/bin` (préfixe inscriptible sans droits admin, déjà dans
>   le `PATH` usuel) — pour viser `/usr/local/bin`, séparez les étapes (voir la
>   procédure manuelle ci-dessous), car `/usr/local` n'est pas inscriptible et
>   `sudo make install` installerait le service pour `root` au lieu de votre
>   session.
> - **Linux** : service systemd **système**, qui requiert `sudo make install`.
>   Le binaire va dans `/usr/local/bin`.
>
> `make uninstall` fait l'inverse (réutilisez le même `PREFIX` qu'à
> l'installation). Variables surchargeables : `PREFIX`, `PORT`, `REFRESH`,
> `LABEL`. Les sections ci-dessous détaillent la procédure manuelle équivalente
> (et le cas Windows, non couvert par `make`).

### Linux — `systemd`

Copiez d'abord le binaire et rendez-le exécutable :

```bash
sudo install -m 0755 dist/go-system-info-linux-amd64 /usr/local/bin/go-system-info
```

Créez ensuite l'unité `/etc/systemd/system/go-system-info.service` :

```ini
[Unit]
Description=go-system-info — métriques système (web/API)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/go-system-info -p 8222 -r 3s
Restart=on-failure
RestartSec=5
# Compte dédié pour un service système. Pour piloter vos propres processus
# depuis l'interface, remplacez par votre identifiant (User=fabien).
User=gosysinfo
Group=gosysinfo
# systemd envoie SIGTERM à l'arrêt → arrêt gracieux géré par le binaire.
KillSignal=SIGTERM
TimeoutStopSec=15
# Durcissement (facultatif mais recommandé pour un service système).
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Activez et démarrez le service :

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now go-system-info.service

# Vérifier / suivre les journaux (slog écrit sur stderr → journald)
systemctl status go-system-info.service
journalctl -u go-system-info.service -f
```

> Pour un **service utilisateur** (sans `sudo`, tourne sous votre session),
> placez le même fichier dans `~/.config/systemd/user/go-system-info.service`
> (sans les lignes `User=`/`Group=`) puis lancez
> `systemctl --user enable --now go-system-info.service`. Ajoutez
> `loginctl enable-linger $USER` pour qu'il démarre sans session ouverte.

### macOS — `launchd`

Sous macOS, l'interface de terminaison ne ciblant que vos processus, le plus
utile est un **LaunchAgent** (tourne sous votre compte, démarre à l'ouverture de
session). Installez le binaire :

```bash
sudo install -m 0755 dist/go-system-info-darwin-arm64 /usr/local/bin/go-system-info
```

Créez `~/Library/LaunchAgents/com.fabien.go-system-info.plist` :

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.fabien.go-system-info</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/go-system-info</string>
        <string>-p</string>
        <string>8222</string>
        <string>-r</string>
        <string>3s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/go-system-info.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/go-system-info.err.log</string>
</dict>
</plist>
```

Chargez l'agent (syntaxe moderne `bootstrap`) :

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.fabien.go-system-info.plist
launchctl enable gui/$(id -u)/com.fabien.go-system-info

# État / arrêt / déchargement
launchctl print gui/$(id -u)/com.fabien.go-system-info
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.fabien.go-system-info.plist
```

> Pour un service **système** démarrant au boot (avant toute session), placez le
> plist dans `/Library/LaunchDaemons/`, faites-le appartenir à `root:wheel`
> (`sudo chown root:wheel …`) et chargez-le avec
> `sudo launchctl bootstrap system …`. Il tournera alors en `root` : la
> terminaison de processus s'appliquera à l'ensemble du système.

### Windows

Le binaire n'implémente pas l'interface native du _Service Control Manager_
(SCM) : `sc.exe create` seul ne suffit donc pas. Deux approches éprouvées :

**Option A — Wrapper de service (recommandé) : NSSM ou WinSW.** Ces outils
enveloppent un exécutable « console » classique en service Windows.

Avec [NSSM](https://nssm.cc/) (PowerShell **administrateur**) :

```powershell
nssm install go-system-info "C:\Program Files\go-system-info\go-system-info.exe"
nssm set go-system-info AppParameters "-p 8222 -r 3s"
nssm set go-system-info Start SERVICE_AUTO_START
# Arrêt propre : envoyer Ctrl+C (géré par le binaire) plutôt que TerminateProcess
nssm set go-system-info AppStopMethodConsole 5000
nssm start go-system-info

# Statut / arrêt / suppression
nssm status go-system-info
nssm stop go-system-info
nssm remove go-system-info confirm
```

> `AppStopMethodConsole` fait envoyer un événement `Ctrl+C` à l'arrêt, ce que le
> binaire intercepte (`os.Interrupt`) pour s'arrêter proprement. Sous Windows,
> `SIGTERM` n'est jamais délivré : ne comptez pas dessus.

**Option B — Planificateur de tâches (sans outil tiers).** Crée une tâche qui
lance le binaire au démarrage de la machine (ce n'est pas un « vrai » service,
mais cela suffit pour un lancement automatique). PowerShell **administrateur** :

```powershell
$action  = New-ScheduledTaskAction -Execute "C:\Program Files\go-system-info\go-system-info.exe" `
                                    -Argument "-p 8222 -r 3s"
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest
Register-ScheduledTask -TaskName "go-system-info" -Action $action `
                       -Trigger $trigger -Principal $principal

# Lancer immédiatement / arrêter / supprimer
Start-ScheduledTask -TaskName "go-system-info"
Stop-ScheduledTask  -TaskName "go-system-info"
Unregister-ScheduledTask -TaskName "go-system-info" -Confirm:$false
```

> Pour piloter vos propres applications depuis l'interface, remplacez
> `-UserId "SYSTEM"` par votre compte (la terminaison ne vise que les processus
> de l'utilisateur exécutant le serveur).

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
    "model_name": "Apple M3",
    "per_core": [24.1, 12.0, 8.3, 5.9, 3.1, 2.0, 0.0, 1.0],
    "temp_celsius": 41.3,
    "temp_label": "PMU tdie3"
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
    "total_gb": 8.58,
    "swap_used_percent": 12.5,
    "swap_used_gb": 0.25,
    "swap_total_gb": 2.0
  },
  "disk": {
    "used_percent": 67.05,
    "used_gb": 164.35,
    "total_gb": 245.1,
    "path": "/",
    "fstype": "apfs"
  },
  "disks": [{ "used_percent": 67.05, "used_gb": 164.35, "total_gb": 245.1, "path": "/", "fstype": "apfs" }],
  "disk_io": {
    "read_bytes_per_sec": 94190.0,
    "write_bytes_per_sec": 905044.0
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

- **`cpu.per_core`** : occupation (0–100) de chaque cœur logique, pour la grille
  par cœur de l'interface. **`cpu.temp_celsius`** / **`cpu.temp_label`** :
  température du capteur le plus chaud et son identifiant. Les capteurs de die
  CPU (« tdie ») sont **privilégiés** quand ils existent, et les références de
  calibration (« tcal », quasi constantes ~50 °C sur Apple Silicon, qui ne sont
  pas des thermomètres) toujours écartées. **Best-effort** : ces champs sont
  **omis** quand aucun capteur n'est exposé (fréquent selon la plateforme, les
  droits, ou un binaire compilé sans cgo).
- **`memory.swap_*`** : occupation du swap (mémoire d'échange) — pourcentage,
  volume utilisé et total. **Best-effort** : sur une machine sans swap actif, les
  trois champs valent `0` (l'interface affiche alors « inactif »).
- **`disks`** : liste des volumes montés significatifs (pour le sélecteur de
  volume de l'interface). Les systèmes de fichiers virtuels et les tout petits
  volumes sont écartés, et les volumes d'un même conteneur qui remontent une
  occupation identique (ex. volumes APFS sur macOS) sont fusionnés ; le volume
  surveillé par défaut (`disk`) y figure toujours. **`fstype`** (présent aussi sur
  `disk`) est le système de fichiers du volume (`apfs`, `ext4`, `ntfs`…).
- **`disk_io`** : débit d'entrées/sorties disque **agrégé sur toutes les unités**
  (octets/s), calculé en différenciant les compteurs cumulés — même principe que
  `net`. C'est une mesure globale, pas par volume.
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
  outil lancé depuis un terminal/IDE est comptabilisé sous celui-ci. Le nom
  affiché est celui de l'**application** : pour un exécutable logé dans un bundle
  macOS, c'est le nom du `.app` le plus externe (« CleanMyMac X » plutôt que
  `com.macpaw.CleanMyMac4.HealthMonitor`), et les racines portant le même libellé
  fusionnent en une seule entrée. `count` est
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
> n'authentifie pas l'appelant. Le flag **`-readonly`** le neutralise
> entièrement (réponse `403`). En mode lecture seule, l'endpoint n'est pas routé
> pour la terminaison.

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

Les processus inaccessibles ou disparus sont simplement absents de la réponse.
Le nombre de PID interrogeables en une requête est borné : au-delà, la liste est
tronquée et la réponse porte `"truncated": true` (l'interface signale alors un
arbre partiel). **Seuls les processus
de l'utilisateur ayant lancé le serveur** sont détaillés : le détail (dont la
ligne de commande, susceptible de contenir des secrets) d'un processus d'autrui
n'est jamais renvoyé.

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
├── biome.json                 # Lint + formatage du front (Biome)
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

## Routes HTTP

| Méthode | Chemin                  | Description                                                     |
| ------- | ----------------------- | --------------------------------------------------------------- |
| `GET`   | `/`                     | Interface web (HTML/CSS/JS embarqué)                            |
| `GET`   | `/api/system`           | Informations système au format JSON                             |
| `GET`   | `/api/stream`           | Flux temps réel (SSE) : système + historique                    |
| `GET`   | `/api/history`          | Historique CPU/mémoire (sparklines)                             |
| `GET`   | `/api/config`           | Configuration de l'interface (intervalle en ms, lecture seule)  |
| `GET`   | `/api/health`           | Sonde de santé (`{"status":"ok"}`)                              |
| `GET`   | `/api/version`          | Version du binaire injectée au build                            |
| `POST`  | `/api/processes/kill`   | Termine des processus (même utilisateur ; `403` si `-readonly`) |
| `GET`   | `/api/processes/detail` | Détail par PID (`?pids=…`, même utilisateur uniquement)         |

## Qualité, tests et CI

- **Tests** : `make test` (avec détecteur de data races). Les handlers sont
  testables sans dépendre de la machine grâce à une interface `systemCollector`
  injectable, et le parsing des flags est isolé dans `parseFlags`.
- **Benchmarks** : `make bench` mesure les fonctions critiques — l'assemblage
  d'un `Info` (coût réel d'une requête) et la copie de l'historique (par
  événement SSE).
- **Lint** : `make lint` (go fmt + go vet) en local ; en CI, `golangci-lint`
  avec la config `.golangci.yml` (jeu `standard` + `bodyclose`/`unconvert`,
  formateurs `gofmt`/`goimports`). Le front (`public/`) est linté et formaté
  par [Biome](https://biomejs.dev/) : `bunx @biomejs/biome check public/`
  (config `biome.json`).
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
