# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Vue d'ensemble

Serveur web Go (module `gosysteminfo`, Go 1.26) qui expose les métriques système
de la machine (CPU, charge, mémoire, disque, réseau, hôte) via une API REST et un
flux SSE, et les affiche dans une interface web sombre embarquée dans le binaire.
Le binaire est **autonome et fonctionne hors-ligne** : l'interface, les polices
`.woff2` et le favicon sont embarqués via `//go:embed public` (`main.go`).

La langue du projet est le **français** : commentaires, messages de log le sont également. Conserver cette convention.

## Commandes

Le `Makefile` regroupe les tâches courantes (variables `PORT=8222`, `REFRESH=3s`) :

```bash
make serve              # go run . -p 8222 -r 3s
make watch              # relance auto au changement (nécessite watchexec)
make test               # go test ./... -race
make test-cover         # tests + rapport de couverture (cover.out)
make bench              # benchmarks (go test -bench=. -benchmem)
make lint               # go fmt ./... + go vet ./...
make build-all          # binaires Linux/macOS(arm64,amd64)/Windows dans dist/
make docker-build       # image Docker multi-stage → scratch
make install            # installe le binaire + le service du système hôte
make uninstall          # désinstalle binaire + service
```

Lancer un seul test :

```bash
go test ./internal/sysinfo -run TestCpuBusyPercent -race
go test ./internal/server  -run TestHandleStream -v
```

`make lint` fait `go fmt` + `go vet`, puis `golangci-lint run` (config
`.golangci.yml`, comme la CI) **si l'outil est installé** — sinon il l'ignore
avec un message. Installer `golangci-lint` pour reproduire la CI localement.

Le front (`public/`) est linté et formaté par **Biome** (config `biome.json` :
espaces, JS en 2, CSS/HTML en 4, largeur 120) : `bunx @biomejs/biome check public/`
(non branché dans la CI). Les `biome-ignore` présents sont **délibérés** (le
`!important` de `[hidden]`, les `div role="group"` des groupes de boutons) — ne
pas les « corriger ».

### Installation en tant que service

`make install`/`make uninstall` détectent l'OS hôte (`uname -s`) et génèrent le
fichier de service à la volée (pas de template versionné) :

- **macOS** → LaunchAgent utilisateur dans `~/Library/LaunchAgents/$(LABEL).plist`,
  chargé via `launchctl bootstrap` (pas de `sudo`). `PREFIX` vaut **`$HOME/.local`
  par défaut** (`/usr/local` n'est pas inscriptible sans admin, et `sudo`
  installerait le LaunchAgent pour `root` au lieu de la session — `install-darwin`
  refuse d'ailleurs de tourner en root).
- **Linux** → unité systemd `/etc/systemd/system/$(BIN_NAME).service`, activée par
  `systemctl enable --now` (nécessite `sudo make install` ; le service tourne sous
  `$SUDO_USER`, pas `root`).
- **Windows** → non couvert par `make` ; documenté dans le README (NSSM ou
  Planificateur de tâches), car le binaire n'implémente pas l'interface SCM native.

Le binaire s'arrête proprement sur `SIGINT`/`SIGTERM` (`os.Interrupt` sous
Windows) — d'où `KillSignal=SIGTERM` côté systemd et l'envoi de `Ctrl+C` côté
NSSM. **Choix de l'utilisateur du service = périmètre de
`POST /api/processes/kill`** : `killOwnedProcess` ne tue que les processus du
compte qui exécute le serveur (cf. README, section « Lancer en tant que
service »). Variables surchargeables : `PREFIX`, `PORT`, `REFRESH`, `LABEL`.

## Architecture

Trois couches, découplées pour la testabilité :

- **`main.go`** — point d'entrée. `parseFlags` (isolé pour être testable sans
  toucher à `flag.CommandLine`) lit `-p`/`-r`, embarque `public/`, puis
  `server.New(cfg).ListenAndServe()`. La variable `version` (défaut `"dev"`) est
  injectée au build via `-ldflags "-X main.version=..."` (voir le Makefile, qui
  calcule la version avec `git describe`).

- **`internal/sysinfo`** — collecte des métriques (dépend de `gopsutil/v4`).
  Deux modes :
    - `Collect()` (fonction libre) fait une mesure CPU **bloquante** sur 500 ms —
      pour un relevé ponctuel.
    - `Collector` (utilisé par le serveur) échantillonne dans des goroutines
      d'arrière-plan (`Start(ctx)`) et met les valeurs en cache, de sorte que
      `Collect()` renvoie **instantanément**. Un sampler par métrique, avec sa
      propre cadence : CPU global (`cpuSampler`), CPU par cœur (`coreSampler`),
      réseau (`netSampler`, 1 s), **E/S disque** (`diskIOSampler`, 1 s, agrégé
      toutes unités — même différenciation de compteurs cumulés que le réseau),
      processus (`procSampler`, 3 s), volumes montés (`diskSampler`, 5 s),
      température (`tempSampler`, 5 s). `History()` expose un anneau circulaire
      thread-safe (~120 points à 1/s, ~2 min) pour les sparklines.

- **`internal/server`** — serveur HTTP, routage et sérialisation JSON. Le
  collecteur est injecté derrière l'interface `systemCollector`, ce qui permet
  aux tests d'utiliser un `stubCollector` sans dépendre de la machine réelle.
  `ListenAndServe` gère les timeouts HTTP et l'arrêt gracieux (SIGINT/SIGTERM).

- **`public/`** — front sans build ni framework. `app.js` consomme `/api/stream`
  (SSE) via `EventSource` ; pas de polling. L'état de connexion (badge « Hors
  ligne » avec délai de grâce) est géré intégralement côté client.
  Mise en page : trois cartes-jauges, puis une **bande d'état** `.card-strip`
  fusionnant Réseau + Hôte (grille de tuiles clé/valeur sur deux lignes ; les
  deux `<ul>` sont en `display: contents` et gardent les id `net-*`/`host-list`
  que `app.js` alimente), puis la carte Processus en pleine largeur. La carte
  CPU est une **ancre native** `<a href="#proc-card">` : clic/Entrée font
  défiler jusqu'aux processus (défilement doux via `scroll-behavior: smooth`,
  débrayé par `prefers-reduced-motion` ; aucun JS impliqué).

### Pièges spécifiques (déjà résolus — ne pas régresser)

- **CPU à 0 % parasite sur macOS** : `cpuSampler.run` ne s'appuie pas sur
  `cpu.Percent` mais différencie lui-même les temps CPU cumulés. Quand les
  compteurs n'ont pas progressé entre deux lectures (relevé « fantôme » fréquent
  sur macOS), la dernière valeur connue est **conservée** plutôt que de publier
  un 0 % trompeur (`cpuBusyPercent` renvoie `moved=false`). Voir le commentaire
  long dans `sysinfo.go`.
- **Jauge CPU lissée (EMA)** : la fenêtre de mesure est courte (500 ms) et
  l'occupation instantanée est très bruitée (saute typiquement de 5 % à 20 %
  d'un relevé à l'autre). `cpuSampler.set` publie donc une **moyenne mobile
  exponentielle** (`cpuSmoothing` = 0,25, soit ≈ 2 s de constante de temps,
  proche de la fenêtre de `top`) plutôt que le relevé brut, sinon la jauge tombe
  souvent sur un creux non représentatif et paraît « trop basse ». Le premier
  relevé amorce la moyenne sans la lisser (`seeded`). Ne pas remplacer par le
  relevé brut. La valeur moyenne reste fidèle à `top` (vérifié) ; c'est la
  variance, pas un biais, qui était en cause.
- **Comptage CPU Linux** : `cpuAllBusy` retire `Guest`/`GuestNice` du total sous
  Linux uniquement (ils sont déjà inclus dans `User`/`Nice`).
- **Température : préférer les capteurs de die** : sur Apple Silicon, le capteur
  le plus chaud est souvent « PMU tcal », une **référence de calibration** quasi
  constante (~50 °C) qui n'est pas un thermomètre — la valeur affichée dépassait
  alors de ~10 °C celle des moniteurs usuels. `hottestTemp` préfère donc les
  capteurs « tdie » quand ils existent et écarte toujours les « tcal » du max.
  Ne pas revenir à un simple max sur tous les capteurs.
- **Température : Mac Intel (darwin/amd64)** : gopsutil décode mal les capteurs
  SMC sur Intel — son `getTemperature` renvoie `0` précisément pour le format
  `sp78` qu'utilisent les thermomètres, donc _tous_ les capteurs ressortent à
  0 °C et le champ disparaît (alors qu'Apple Silicon passe, lui, par l'API HID
  IOKit et fonctionne). `readHottestTemp` est donc **spécifique à la plateforme** :
  `temp_generic.go` (`!darwin || arm64`) délègue à gopsutil (via `hottestTemp`) ;
  `temp_darwin_amd64.go` rouvre l'AppleSMC via **purego** (pas de cgo —
  `CGO_ENABLED=0` reste valable) et décode `sp78`/`flt` correctement. Le
  `dataType`/`dataSize` d'une clé n'est renseigné que par l'appel `GetKeyInfo`,
  **pas** par `ReadKey` (piège : décoder d'après la réponse de lecture donne 0).
  Ne pas re-router l'Intel vers `sensors.SensorsTemperatures()`.
    - **Choix de la sonde CPU sur Intel** : ne PAS prendre le max global. Les sondes
      par cœur (`TCxC`) et le PECI (`TCXC`) lisent ~10-15 °C plus chaud que ce
      qu'affichent les moniteurs usuels (CleanMyMac, iStat), qui montrent la
      proximité/die (`TC0P`/`TC0D`). `readHottestTemp` retient donc la **première**
      clé lisible et non aberrante de `cpuTempKeys`, ordonnée par préférence
      (proximité/die d'abord, sondes chaudes en dernier recours). Ne pas revenir à
      un max sur toutes les sondes.
- **SSE et WriteTimeout** : `handleStream` neutralise le `WriteTimeout` du serveur
  pour la connexion longue via `http.NewResponseController`. Le `statusRecorder`
  implémente `Unwrap()` pour que `Flush`/`SetWriteDeadline` traversent le wrapper.
- **CPU navigateur au repos** : `app.js` évite toute animation continue (pulse
  ponctuel, halo en `box-shadow` plutôt qu'un `drop-shadow` SVG recalculé). Ne
  pas réintroduire d'animations permanentes.
- **CPU des processus** : `aggregateProcesses` calcule le CPU par **delta** des
  temps cumulés (le `CPUPercent()` de gopsutil ne donne qu'une moyenne depuis le
  démarrage). La valeur est un **% d'un cœur** (façon `top`/`htop`, peut dépasser
  100 % en multi-thread) — ne pas la « normaliser » sur le nombre de cœurs, ce
  qui écrase les valeurs et ne correspond à aucun moniteur usuel.
- **Regroupement par application (arbre)** : `aggregateProcesses` rattache chaque
  processus à son ancêtre de plus haut niveau via `rootAncestor` (remontée des
  `ppid` jusqu'à un enfant de launchd/pid 1) et somme tout le sous-arbre sous le
  nom de la racine. Conséquence assumée : un outil lancé depuis un terminal/IDE
  est comptabilisé sous celui-ci (ex. `claude` sous `zed`). Relevé espacé
  (`procSampleInterval`, 3 s) car l'énumération est coûteuse.
- **Libellé = nom d'application** : `appLabel` remplace le nom du binaire par le
  nom du bundle `.app` **le plus externe** du chemin de l'exécutable (best-effort
  via `p.Exe()`) — « CleanMyMac X » plutôt que `com.macpaw.CleanMyMac4.Menu`.
  Les groupes sont indexés **par libellé** (et non par PID de racine) : les
  racines d'une même application (LoginItems, agents…) fusionnent en une seule
  entrée — sinon la liste montre des doublons et la sélection par nom du front
  devient ambiguë. Le panneau de détails garde, lui, les noms bruts par PID.
- **Terminaison de processus** : `killOwnedProcess` n'envoie SIGTERM qu'aux
  processus de l'utilisateur courant (revérifié par PID au moment du kill, pas
  d'après le cache). Ne pas relâcher ce garde-fou.
- **Carte Processus — sélection & arbre** : la liste se réordonne à chaque relevé.
  Le front suit l'application sélectionnée **par nom** (`selectedProc`), pas par
  position, et place la terminaison dans un **panneau latéral** fixe (`.proc-body.with-detail`)
  — ne pas remettre de bouton de kill par ligne (cible mouvante). Le panneau
  reconstruit l'**arbre** parent → enfants depuis `/api/processes/detail`
  (`ppid`/`name`), avec terminaison par nœud (`killNode` → sous-arbre). Rechargé
  seulement quand l'ensemble des PID du groupe change.
- **Dédup des volumes indépendante de l'ordre** : `selectVolumes` ajoute le volume
  par défaut **en premier** et pré-enregistre sa signature d'occupation, pour que
  ses volumes-frères d'un même conteneur APFS (macOS : `/System/Volumes/Data`…)
  fusionnent vers lui **quel que soit l'ordre** de `disk.Partitions`. Sinon un
  frère listé avant `/` survivait à la dédup, puis `/` était ajouté en plus (entrée
  redondante) → le sélecteur de volume apparaissait à tort. Ne pas revenir à une
  dédup dépendante de l'ordre.
- **Attribut HTML `hidden` vs `display` CSS** : règle globale
  `[hidden] { display: none !important }` dans `styles.css`. Sans elle, un
  `display:block` d'une classe (`.vol-select`, `.version`…) l'emporte sur le
  `[hidden]{display:none}` du navigateur (même spécificité, l'auteur gagne) et
  l'élément reste affiché — souvent **vide** — malgré `el.hidden = true`. C'était
  la cause du sélecteur de volume affiché vide. Ne pas retirer cette règle.
- **Cache des assets embarqués** : servis avec `Cache-Control: no-cache`
  (`staticCacheControl` dans `server.go`). `embed.FS` n'expose pas de date de
  modification, donc `http.FileServer` n'émet ni `Last-Modified` ni `ETag` : sans
  en-tête, le navigateur applique un cache heuristique et peut resservir un ancien
  `app.js`/`styles.css` après reconstruction du binaire (bundle désynchronisé).
- **Cartes-jauges à hauteur égale** : `.card-gauge` est une colonne flex étirée à
  la hauteur de la plus remplie (CPU, avec sa grille par cœur + température). Pour
  éviter un vide, les cartes RAM/Disque comblent l'espace avec des infos utiles
  ancrées en bas (`.card-extra { margin-top: auto }`) : **Swap** (RAM), **système
  de fichiers + débit d'E/S** (Disque). Jauge en haut → cercles alignés d'une
  carte à l'autre. Ne pas réintroduire de barre d'occupation disque (jugée
  redondante avec l'anneau).

## Endpoints

`/api/system` (JSON ponctuel — inclut notamment `cpu.per_core`/`cpu.temp_*`,
`memory.swap_*`, `disk.fstype`, `disk_io` (débit d'E/S agrégé), `disks` (volumes)),
`/api/stream` (SSE `{system, history}`), `/api/history`,
`/api/config` (`refresh_ms` et `readonly` pour le front), `/api/health`,
`/api/version`, `POST /api/processes/kill` (termine des PID — **uniquement** ceux
de l'utilisateur ayant lancé le serveur ; garde-fou dans `killOwnedProcess` ;
`403` si `-readonly`), `GET /api/processes/detail?pids=…` (détail par PID,
restreint au propriétaire ; alimente le panneau de détails du front). Détails et
exemples de réponses dans le README.
