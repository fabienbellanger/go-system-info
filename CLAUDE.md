# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Vue d'ensemble

Serveur web Go (module `gosysteminfo`, Go 1.26) qui expose les métriques système
de la machine (CPU, charge, mémoire, disque, réseau, hôte) via une API REST et un
flux SSE, et les affiche dans une interface web sombre embarquée dans le binaire.
Le binaire est **autonome et fonctionne hors-ligne** : l'interface, les polices
`.woff2` et le favicon sont embarqués via `//go:embed public` (`main.go`).

La langue du projet est le **français** : commentaires, messages de log et de
commit le sont également. Conserver cette convention.

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
```

Lancer un seul test :

```bash
go test ./internal/sysinfo -run TestCpuBusyPercent -race
go test ./internal/server  -run TestHandleStream -v
```

`make lint` ne fait que `go fmt` + `go vet` en local. La CI exécute en plus
`golangci-lint` (config `.golangci.yml`) — lancer `golangci-lint run` localement
avant de pousser si l'outil est installé.

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
  - `Collector` (utilisé par le serveur) échantillonne CPU et réseau dans des
    goroutines d'arrière-plan (`Start(ctx)`) et met les valeurs en cache, de
    sorte que `Collect()` renvoie **instantanément**. `History()` expose un
    anneau circulaire thread-safe (~120 points à 1/s, ~2 min) pour les sparklines.

- **`internal/server`** — serveur HTTP, routage et sérialisation JSON. Le
  collecteur est injecté derrière l'interface `systemCollector`, ce qui permet
  aux tests d'utiliser un `stubCollector` sans dépendre de la machine réelle.
  `ListenAndServe` gère les timeouts HTTP et l'arrêt gracieux (SIGINT/SIGTERM).

- **`public/`** — front sans build ni framework. `app.js` consomme `/api/stream`
  (SSE) via `EventSource` ; pas de polling. L'état de connexion (badge « Hors
  ligne » avec délai de grâce) est géré intégralement côté client.

### Pièges spécifiques (déjà résolus — ne pas régresser)

- **CPU à 0 % parasite sur macOS** : `cpuSampler.run` ne s'appuie pas sur
  `cpu.Percent` mais différencie lui-même les temps CPU cumulés. Quand les
  compteurs n'ont pas progressé entre deux lectures (relevé « fantôme » fréquent
  sur macOS), la dernière valeur connue est **conservée** plutôt que de publier
  un 0 % trompeur (`cpuBusyPercent` renvoie `moved=false`). Voir le commentaire
  long dans `sysinfo.go`.
- **Comptage CPU Linux** : `cpuAllBusy` retire `Guest`/`GuestNice` du total sous
  Linux uniquement (ils sont déjà inclus dans `User`/`Nice`).
- **SSE et WriteTimeout** : `handleStream` neutralise le `WriteTimeout` du serveur
  pour la connexion longue via `http.NewResponseController`. Le `statusRecorder`
  implémente `Unwrap()` pour que `Flush`/`SetWriteDeadline` traversent le wrapper.
- **CPU navigateur au repos** : `app.js` évite toute animation continue (pulse
  ponctuel, halo en `box-shadow` plutôt qu'un `drop-shadow` SVG recalculé). Ne
  pas réintroduire d'animations permanentes.

## Endpoints

`/api/system` (JSON ponctuel), `/api/stream` (SSE `{system, history}`),
`/api/history`, `/api/config` (`refresh_ms` pour le front), `/api/health`,
`/api/version`. Détails et exemples de réponses dans le README.
