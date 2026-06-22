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

13. **CI GitHub Actions** : `go test -race`, `go vet`, build multi-plateforme via le Makefile existant.
14. **Dockerfile** multi-stage (build statique `CGO_ENABLED=0` déjà en place → image `scratch` minuscule).
15. **`golangci-lint`** en complément du `go vet` actuel.
16. **Benchmarks** : benchmarker les fonctions critiques (`Collect()`) pour évaluer les performances.

## Tests

17. Couvrir le **parsing des flags** et le **cas d'erreur de `handleSystem`** (injecter un collecteur via une interface plutôt que d'appeler `sysinfo.Collect()` en dur dans `server.go:51` — ça rend le handler testable sans dépendre de la vraie machine).
