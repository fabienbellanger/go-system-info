# Plan d'amélioration

Le projet est déjà propre et bien structuré (packages séparés, tests, Makefile multi-plateforme). Voici des pistes concrètes, classées par priorité, en m'appuyant sur le code actuel.

## Robustesse serveur (le plus important)

1. ~~**Timeouts HTTP manquants**~~ — ✅ **Fait.** `ListenAndServe` construit désormais un `&http.Server{}` avec `ReadTimeout`, `WriteTimeout` et `IdleTimeout` (constantes dans `server.go`), protégeant contre les connexions lentes/Slowloris.

2. ~~**Arrêt gracieux (graceful shutdown)**~~ — ✅ **Fait.** Écoute de `SIGINT`/`SIGTERM` via `signal.NotifyContext`, puis `srv.Shutdown(ctx)` borné par `shutdownTimeout` pour laisser les requêtes en cours se terminer.

3. ~~**Le CPU bloque chaque requête 500 ms**~~ — ✅ **Fait.** Introduction d'un `sysinfo.Collector` : une goroutine (`cpuSampler.run`) échantillonne le CPU en arrière-plan via `cpu.Percent(0, …)` (non bloquant) et met le résultat en cache derrière un `sync.RWMutex`. Les requêtes `GET /api/system` sont désormais instantanées et la charge de mesure reste constante. La fonction package-level `Collect()` (mesure synchrone) est conservée pour un relevé ponctuel.

## Observabilité

4. ~~**Endpoint `/api/health`**~~ — ✅ **Fait.** Route `GET /api/health` renvoyant `{"status":"ok"}` (`handleHealth`) pour les sondes des orchestrateurs.

5. ~~**Logging des requêtes**~~ — ✅ **Fait.** Middleware `logRequests` enveloppant le `mux` : journalise méthode, chemin, statut (capturé via `statusRecorder`), durée et adresse distante. Tout le serveur (`main.go` inclus) est passé à `log/slog` pour des logs structurés.

6. ~~**Injection de la version au build**~~ — ✅ **Fait.** Variable `main.version` (défaut `"dev"`) injectée via `-ldflags "-X main.version=$(VERSION)"` dans le Makefile, où `VERSION` provient de `git describe --tags --always --dirty`. Exposée par l'endpoint `GET /api/version` (`Config.Version`) et journalisée au démarrage.

## L'interface se prétend « autonome » mais ne l'est pas

7. ~~**Dépendance aux Google Fonts**~~ — ✅ **Fait.** Les sous-ensembles latin/latin-ext d'Inter et JetBrains Mono (fontes variables, 4 fichiers `.woff2`) sont embarqués dans `public/fonts/` et déclarés via `@font-face` dans `styles.css`. Les `<link>` vers `fonts.googleapis.com`/`fonts.gstatic.com` ont été retirés d'`index.html` : l'app ne fait plus aucun appel réseau externe et fonctionne hors-ligne.
8. ~~**Pas de favicon**~~ — ✅ **Fait.** Ajout d'un `public/favicon.svg` (écran stylisé au dégradé du thème) référencé via `<link rel="icon" type="image/svg+xml">`, supprimant le 404 systématique.

## Fonctionnalités produit

9. ~~**Sparklines / historique**~~ — ✅ **Fait.** Le `Collector` enregistre un anneau circulaire thread-safe (`history`, 120 points à 1 point/s, soit ≈ 2 min) via une goroutine `recordHistory` lancée par `Start`. Chaque point (`HistorySample{CPU, Mem}`) combine l'utilisation CPU mise en cache et la mémoire relevée à l'instant. L'endpoint `GET /api/history` (`handleHistory`) renvoie la série ordonnée. Côté interface, `renderSparkline` (`app.js`) trace une courbe d'évolution (aire + ligne SVG, couleur suivant le seuil) sous les jauges CPU et RAM, rafraîchie à chaque cycle via `updateHistory`.
10. ~~**SSE ou WebSocket** au lieu du polling `setInterval`~~ — ✅ **Fait.** Choix des **Server-Sent Events** (pas de dépendance, push serveur→client, reconnexion native). Endpoint `GET /api/stream` (`handleStream`) qui émet un premier événement aussitôt puis un à chaque intervalle (`s.cfg.Refresh`), poussant un état combiné `streamState{System, History}` — une seule connexion remplace les deux fetchs `/api/system` + `/api/history` par cycle. `http.ResponseController.SetWriteDeadline(zéro)` neutralise le `WriteTimeout` du serveur pour cette connexion longue (sinon coupée à 15 s), et `statusRecorder.Unwrap()` permet à `Flush`/`SetWriteDeadline` de traverser le middleware de logging. Côté interface, `app.js` remplace `setInterval` par un `EventSource` (`connect`) dont chaque message alimente `applyState` (jauges, hôte, sparklines). Les endpoints REST `/api/system` et `/api/history` restent disponibles.
11. ~~**Métriques supplémentaires**~~ — ✅ **Fait (load average + débit réseau).** `load.Avg()` alimente `Info.Load` (1/5/15 min) via `collectLoad`, lu de façon synchrone et non bloquante. Le débit réseau (`Info.Net`, octets/s ↑/↓) est calculé par un `netSampler` en arrière-plan qui différencie les compteurs cumulés de `net.IOCounters` entre deux relevés espacés (`netSampleInterval`), avec garde contre la réinitialisation des compteurs (`perSec`) ; logique de taux isolée dans `netRate` (testée). Côté interface : nouvelle carte « Réseau » (⬇️/⬆️, `formatRate` en unités décimales) et ligne « Charge » dans la carte Hôte (`formatLoad`). **Écartés** : top processus (échantillonnage CPU par process, trop coûteux à chaque cycle) et température/batterie (`SensorsTemperatures` peu fiable sur macOS sans cgo, batterie non exposée par gopsutil).
12. **Partition disque configurable** — la note « espace purgeable non inclus » (`app.js:60`) est codée en dur pour macOS. Un flag `-d /chemin` (le `Collect()` accepte déjà un path en interne) et le support de plusieurs montages généraliseraient l'outil.

## Industrialisation

13. ~~**CI GitHub Actions**~~ — ✅ **Fait.** Workflow `.github/workflows/ci.yml` (déclenché sur push `main` et pull requests, avec annulation des runs concurrents) en trois jobs : **test** (`go vet` + `go test -race`), **lint** (`golangci-lint`) et **build** (`make build-all`, cross-compilation 4 plateformes). Version de Go lue depuis `go.mod` (`go-version-file`), cache Go activé, `fetch-depth: 0` pour que `git describe` versionne correctement.
14. ~~**Dockerfile**~~ — ✅ **Fait.** `Dockerfile` multi-stage : étape `golang:1.26-alpine` (téléchargement des deps en couche cachée puis build statique `CGO_ENABLED=0`, version injectable via `--build-arg VERSION`), image finale `FROM scratch` (binaire seul, l'interface web étant embarquée et aucun appel réseau externe). `EXPOSE 8222`, `ENTRYPOINT ["/app"]`, surcharge des flags possible. `.dockerignore` réduit le contexte de build. _(Build non validé localement : daemon Docker arrêté.)_
15. ~~**`golangci-lint`**~~ — ✅ **Fait.** `.golangci.yml` (format v2) : jeu `standard` (errcheck, govet, ineffassign, staticcheck, unused) + `bodyclose`/`unconvert`, formateurs `gofmt`/`goimports`. `misspell` volontairement écarté (anglais uniquement → faux positifs sur les commentaires français). Code validé contre staticcheck/errcheck/ineffassign/unconvert/goimports (un `res.Body.Close()` non vérifié corrigé au passage).
16. ~~**Benchmarks**~~ — ✅ **Fait.** `BenchmarkCollect` (assemblage d'un `Info` = coût réel d'une requête) et `BenchmarkHistorySnapshot` (copie de l'historique par événement SSE) dans `sysinfo_test.go`, plus la cible `make bench` (`go test -bench=. -benchmem -run=^$`). À noter : le bench révèle que `collect()` coûte ~34 ms/op, dominé par `cpu.Info()` (lecture du modèle CPU à chaque appel) — piste d'optimisation future (mise en cache du modèle).

## Tests

17. ~~**Parsing des flags + cas d'erreur de `handleSystem`**~~ — ✅ **Fait.** `parseFlags` extrait de `main` (propre `FlagSet`, sortie injectable) et couvert par `main_test.go` (défauts, valeurs custom, port invalide, flag inconnu). Côté serveur, introduction de l'interface `systemCollector` (`Start`/`Collect`/`History`) portée par `Server.collector` : un `stubCollector` de test injecte une erreur et `TestHandleSystemError` vérifie la réponse `500`, sans dépendre de la vraie machine. Au passage, correction du Makefile dont les cibles `build-*` étaient cassées (espaces non échappés dans `BIN_NAME`).
