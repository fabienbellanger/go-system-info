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

9. **Sparklines / historique** — garder un anneau circulaire des N dernières mesures côté serveur et tracer une courbe d'évolution CPU/RAM. Gros gain visuel.
10. **SSE ou WebSocket** au lieu du polling `setInterval` (`app.js:97`) — push temps réel, plus léger.
11. **Métriques supplémentaires** : load average, débit réseau, température/batterie (utile sur macOS), top processus. `gopsutil` expose déjà `load`, `net`, `process`.
12. **Partition disque configurable** — la note « espace purgeable non inclus » (`app.js:60`) est codée en dur pour macOS. Un flag `-d /chemin` (le `Collect()` accepte déjà un path en interne) et le support de plusieurs montages généraliseraient l'outil.

## Industrialisation

13. **CI GitHub Actions** : `go test -race`, `go vet`, build multi-plateforme via le Makefile existant.
14. **Dockerfile** multi-stage (build statique `CGO_ENABLED=0` déjà en place → image `scratch` minuscule).
15. **`golangci-lint`** en complément du `go vet` actuel.

## Tests

16. Couvrir le **parsing des flags** et le **cas d'erreur de `handleSystem`** (injecter un collecteur via une interface plutôt que d'appeler `sysinfo.Collect()` en dur dans `server.go:51` — ça rend le handler testable sans dépendre de la vraie machine).
