# Rapport d'analyse de code — go-system-info

> Analyse statique du 2026-07-02, branche `main` (commit `fc91eeb`).
> Périmètre : `main.go`, `internal/sysinfo`, `internal/server`, front `public/`,
> outillage (`Makefile`, `Dockerfile`, CI).
> Les tests (`go test ./... -race`) et `go vet ./...` passent au moment de l'analyse.
>
> Analyse **statique** : les constats s'appuient sur la lecture du code, sur les
> contrats d'API documentés (ex. `time.NewTicker`, preflight CORS) et,
> ponctuellement, sur la source des dépendances (gopsutil) ; ils n'ont pas été
> reproduits dynamiquement.
>
> _Ce rapport fusionne deux passes d'analyse successives (leurs constats étaient
> complémentaires)._

## Synthèse

Projet de **très bonne facture** : architecture en couches découplées, injection
de dépendances pour la testabilité, thread-safety soignée (mutex, anneau
circulaire), arrêt gracieux, échantillonnage en arrière-plan, front vanilla
soigné (accessibilité, `prefers-reduced-motion`, pas d'animation continue). Les
pièges connus (0 % CPU parasite macOS, EMA, comptage Guest/GuestNice Linux) sont
documentés et testés.

Restent quelques défauts **réels** — deux méritent une correction rapide (une
panique déclenchable par configuration, un CSRF sur l'action destructrice) — et
un ensemble d'améliorations de robustesse, de performance et de finition.

| Priorité   | Constat                                                                   | Type                 | Emplacement                    |
| ---------- | ------------------------------------------------------------------------- | -------------------- | ------------------------------ |
| 🔴 Haute   | `-r 0s` (ou négatif) fait paniquer chaque requête `/api/stream`           | Bug                  | `main.go:57` · `server.go:236` |
| 🔴 Haute   | CSRF sur `POST /api/processes/kill` (Content-Type non vérifié)            | Sécurité             | `server.go:146`                |
| 🟠 Moyenne | Arrêt gracieux bloqué ~10 s par tout client SSE, puis sortie en code 1    | Bug                  | `server.go:81,239`             |
| 🟠 Moyenne | Disque : chemin figé à `/`, trompeur sous Windows, non configurable       | Portabilité          | `sysinfo.go:174,854`           |
| 🟠 Moyenne | Erreur système transitoire → coupure du flux SSE                          | Robustesse           | `server.go:254`                |
| 🟠 Moyenne | `cpu.Info()` + `host.Info()` rejoués à chaque relevé                      | Performance          | `sysinfo.go:808,827`           |
| 🟠 Moyenne | Injection HTML (`innerHTML`) des champs hôte                              | Sécurité / cohérence | `app.js:578-585`               |
| 🟡 Basse   | Corps de requête `/kill` non borné                                        | Sécurité             | `server.go:154`                |
| 🟡 Basse   | Écoute `0.0.0.0` non restreignable par flag                               | Sécurité             | `server.go:88`                 |
| 🟡 Basse   | Front : échecs de terminaison ignorés (`killPids` ne lit pas `results[]`) | Bug                  | `app.js:396`                   |
| 🟡 Basse   | Front : clé d'instances mémorisée avant succès du fetch                   | Bug                  | `app.js:236`                   |
| 🟡 Basse   | Front : course de réponses dans `loadInstances`                           | Bug                  | `app.js:244`                   |
| 🟡 Basse   | `ProcessInfo.User` : commentaire en désaccord avec le code                | Qualité              | `sysinfo.go:66,727`            |
| 🟡 Basse   | `Username()` relu par processus à chaque énumération                      | Performance          | `sysinfo.go:663`               |
| 🟡 Basse   | Endpoints GET n'imposent pas la méthode HTTP                              | Qualité              | `server.go:65-72`              |
| 🟡 Basse   | En-tête `Connection: keep-alive` posé manuellement                        | Qualité              | `server.go:225`                |

---

## 1. Bugs et défauts de robustesse

### 1.1 🔴 Panique de `/api/stream` avec un intervalle nul ou négatif

`parseFlags` (`main.go:57`) accepte n'importe quelle durée pour `-r`, y compris
`0s` ou `-5s`. Or `handleStream` fait `time.NewTicker(s.cfg.Refresh)`
(`server.go:236`), et **`time.NewTicker` panique pour une durée ≤ 0**. `net/http`
rattrape la panique par requête (le serveur survit), mais chaque connexion
`/api/stream` meurt immédiatement : l'interface reste indéfiniment « Hors ligne »
sans message exploitable.

**Correctif.** Valider la configuration au démarrage — `Refresh > 0` (idéalement
avec un plancher, ex. 250 ms : un intervalle de 1 ms ferait tourner collecte +
sérialisation en boucle serrée) et `Port ∈ [1, 65535]`. À placer dans
`parseFlags` ou `server.New`.

### 1.2 🟠 Chemin de disque figé à `/` — trompeur sous Windows, non configurable

`collect()` appelle `info.collectDisk("/")` en dur (`sysinfo.go:174`), transmis
tel quel à `disk.Usage(path)` (`sysinfo.go:854`) :

```go
if err := info.collectDisk("/"); err != nil {
    return nil, err
}
```

Correct sur macOS/Linux. Mais le projet **cible aussi Windows**
(`make build-windows`, section « Service » du README), où `/` n'est pas une
racine de volume.

Vérification faite dans la source de la dépendance (gopsutil v4.26.5,
`disk/disk_windows.go`) : `UsageWithContext` passe le chemin tel quel à l'API
Win32 `GetDiskFreeSpaceExW`, qui accepte les barres obliques et résout `/` vers
la racine du **lecteur courant**. Pas de plantage ni de `500` donc, mais deux
défauts réels :

- les statistiques affichées sont celles du **volume du répertoire de travail du
  process** (variable selon la façon dont le service est lancé), présentées sous
  l'étiquette « / » ;
- le montage surveillé n'est pas configurable, même sur Unix (impossible de
  suivre un autre volume que la racine).

**Correctif.** Choisir le chemin par défaut selon `runtime.GOOS` (`C:\` sous
Windows, `/` ailleurs) et/ou exposer un flag `-disk`.

### 1.3 🟠 Arrêt gracieux retardé par les clients SSE, puis sortie en erreur

`http.Server.Shutdown` attend que les connexions actives redeviennent inactives
mais **n'annule pas les contextes des requêtes en cours**. La boucle de
`handleStream` ne sort que sur `r.Context().Done()` (déconnexion client) ou
erreur d'écriture. Conséquence : `Ctrl+C` avec un seul onglet ouvert bloque
l'arrêt pendant tout le `shutdownTimeout` (10 s), puis `Shutdown` renvoie
`context.DeadlineExceeded` — `main` journalise « échec du serveur » et sort en
**code 1**, alors que c'est un arrêt normal.

**Correctif.** Lier les requêtes au contexte de signal déjà créé dans
`ListenAndServe` (`server.go:81`) :
`srv.BaseContext = func(net.Listener) context.Context { return ctx }`. Les
`r.Context()` des flux SSE seront annulés dès SIGINT/SIGTERM et `Shutdown` se
terminera immédiatement.

### 1.4 🟠 Une erreur système transitoire ferme le flux SSE

Dans `writeStreamEvent` (`server.go:254`), toute erreur de `collector.Collect()`
fait retourner l'erreur, ce qui termine `handleStream` et **ferme la connexion**.
`Collect()` échoue dès que `host.Info()`, `mem.VirtualMemory()` **ou**
`disk.Usage()` échoue (elles remontent l'erreur, contrairement à `load.Avg()` /
`cpu.Info()` qui sont tolérées). Un hoquet ponctuel suffit à rompre le flux. Le
client `EventSource` se reconnecte, donc l'impact est limité, mais c'est fragile
pour une métrique non essentielle.

**Correctif.** Rendre `collectDisk`/`collectHost`/`collectMemory` tolérants
(conserver la dernière valeur connue / champ partiel) plutôt que d'avorter tout
le relevé ; ou, dans `writeStreamEvent`, logguer et émettre un événement partiel.

### 1.5 🟡 Front — échecs de terminaison silencieusement ignorés

`handleKill` renvoie toujours `200`, les échecs étant rapportés dans
`results[].ok`. Or `killPids` (`app.js:396`) ne teste que `res.ok` (le statut
HTTP) : si toutes les terminaisons échouent (processus disparu, refus),
`killSelected` referme quand même le panneau comme après un succès.

**Correctif.** Inspecter `results` dans la réponse et signaler les `ok:false`.

### 1.6 🟡 Front — clé d'instances mémorisée avant le succès du fetch

Dans `syncProcDetail` (`app.js:236`), `loadedInstancesKey = key` est posé
**avant** que `loadInstances` réussisse. Si le fetch échoue (coupure ponctuelle),
le panneau affiche « Détails indisponibles » et **ne retentera jamais** tant que
l'ensemble des PID ne change pas.

**Correctif.** Ne mémoriser la clé qu'après succès, ou la réinitialiser dans le
`catch`.

### 1.7 🟡 Front — course de réponses dans `loadInstances`

Deux sélections rapprochées déclenchent deux fetch concurrents vers
`/api/processes/detail` ; la réponse la plus lente peut écraser la plus récente
dans le même conteneur (`app.js:244`).

**Correctif.** Jeton de génération, ou `AbortController` annulant la requête
précédente.

### 1.8 Points vérifiés — **pas** de bug

Anneau circulaire (`TestHistoryRingBuffer`), débit réseau (compteur réinitialisé
/ durée nulle), garde-fou de terminaison (revérification du propriétaire par PID
au moment du kill), delta CPU (PID neuf/recyclé → 0 %), anti-cycle
(`rootAncestor` borné à 64, `renderTree` borné à 32 + `seen`). ✅

---

## 2. Sécurité

Le serveur est un outil personnel, mais il expose une action **destructrice**
(terminaison de processus) : le durcissement mérite d'être traité.

### 2.1 🔴 CSRF sur `POST /api/processes/kill`

`handleKill` (`server.go:146`) décode le corps via
`json.NewDecoder(r.Body).Decode(&req)` **sans vérifier le `Content-Type`**. Un
site malveillant visité par la victime peut donc envoyer une **requête « simple »**
(`Content-Type: text/plain`, sans en-tête personnalisé) vers
`http://localhost:8222/api/processes/kill` : elle **ne déclenche pas de preflight
CORS**, le navigateur l'émet directement, et `json.Decode` la parse sans se
soucier du type MIME. Le garde-fou `killOwnedProcess` limite _quels_ processus
sont concernés (ceux de l'utilisateur du serveur), mais **pas _qui_ peut
déclencher la demande**. Un utilisateur naviguant sur une page piégée pendant que
le serveur tourne peut ainsi voir ses propres processus terminés.

**Correctif.** Au choix / cumulables :

- rejeter si `Content-Type` ≠ `application/json` (casse la requête simple) ;
- vérifier `Sec-Fetch-Site: same-origin` ou l'en-tête `Origin` ;
- exiger un en-tête personnalisé (force le preflight) ou un jeton anti-CSRF.

### 2.2 🟠 Injection HTML des champs hôte via `innerHTML`

Le front est globalement rigoureux (`buildProcRow` insère les chaînes système via
`textContent`, comme l'affirme son commentaire). **Mais** `applyState` construit
la liste « Hôte » par interpolation dans `innerHTML` (`app.js:578-585`) :

```js
document.getElementById("host-list").innerHTML = `
  <li>…<span class="val">${host.hostname || "—"}</span></li>
  <li>…<span class="val">${host.platform || host.os || "—"}</span></li>
  …`;
```

`hostname`, `platform`, `os`, `kernel_arch` proviennent du système. Un hostname
contenant du HTML (`<img src=x onerror=…>`) serait interprété. Surface faible
(il faut contrôler le hostname de la machine), mais **incohérent avec la posture
de sécurité tenue ailleurs** dans le même fichier.

**Correctif.** Construire ces `<li>` via `createElement` + `textContent`.

### 2.3 🟡 Corps de requête non borné sur `/api/processes/kill`

`handleKill` décode le corps sans `http.MaxBytesReader` (`server.go:154`), et le
nombre de PID n'est pas plafonné (contrairement à `/api/processes/detail` qui
borne à `maxDetailPIDs = 128`). Un client peut soumettre un corps volumineux.

**Correctif.** Envelopper `r.Body` dans `http.MaxBytesReader` et borner
`len(req.PIDs)`.

### 2.4 🟡 Écoute sur toutes les interfaces, non restreignable

`ListenAndServe` écoute sur `:port` (`server.go:88`), soit `0.0.0.0`. Le README
documente correctement le risque (lignes 520-523 : « si vous exposez le serveur
au-delà de `localhost`, protégez-le en amont »), mais **aucun flag ne permet de
restreindre l'écoute à `127.0.0.1`** sans reverse-proxy/pare-feu externe. Sans ce
garde-fou, tout le réseau local peut lire `/api/processes/detail` (qui expose les
`cmdline`, parfois porteuses de secrets passés en argument) et, combiné à 2.1,
solliciter le kill.

**Correctif.** Ajouter un flag `-host` (défaut sûr `127.0.0.1` pour un moniteur
de poste ; opt-in explicite pour l'exposition réseau).

### 2.5 Absence d'authentification — acceptable dans le contexte

Pas d'auth : cohérent pour un outil local si l'écoute est restreinte à localhost
(2.4) et le CSRF corrigé (2.1). Le README assume ce choix. Rien à ajouter au-delà
de 2.1 / 2.4.

---

## 3. Performance

### 3.1 🟠 Métadonnées statiques rejouées à chaque relevé

`collect()` est sur le chemin chaud (chaque tick SSE **et** chaque
`GET /api/system`, multiplié par le nombre de clients). Or il rappelle
systématiquement des données quasi-immuables :

- `host.Info()` (`sysinfo.go:808`) — hostname, OS, plateforme, arch (seul
  `Uptime` évolue) ;
- `cpu.Info()` (`sysinfo.go:827`) — modèle CPU : constant, et appel notoirement
  coûteux (`sysctl` / `/proc/cpuinfo`).

Le collecteur met déjà en cache %CPU, réseau et processus, mais **pas** ces
métadonnées.

**Correctif.** Résoudre une fois au démarrage (comme `currentUser`) les champs
constants — modèle CPU, cœurs, hostname/OS/arch — et ne recalculer par relevé que
ce qui bouge (uptime, %CPU, mémoire, disque, réseau).

### 3.2 🟡 `Username()` relu par processus à chaque énumération

`readProcs` (`sysinfo.go:663`) appelle `p.Username()` pour **chaque** processus
toutes les `procSampleInterval` (3 s). Sur macOS chaque appel est un `sysctl`
séparé ; avec plusieurs centaines de processus, c'est le poste de coût dominant.
Or l'UID d'un PID donné ne change pas.

**Correctif.** Mémoïser `uid → username`, ou ne résoudre que les PID nouvellement
vus.

### 3.3 Points sains

Échantillonnage découplé des requêtes → réponses instantanées (validé par
`BenchmarkCollect`) ; `snapshot()` en O(120) négligeable ; front sans animation
continue (CPU au repos maîtrisé) ; un seul flux SSE porte état + historique. ✅

---

## 4. Qualité de code

### Points forts

- **Architecture** en trois couches découplées, `systemCollector` injectable.
- **Commentaires** qui expliquent le _pourquoi_ (EMA, relevé fantôme macOS,
  Guest/GuestNice Linux).
- **Tests** ciblés sur la logique délicate, cas limites inclus.
- **Cohérence** de langue et de conventions ; **outillage** complet (Makefile
  riche, CI vet + race + golangci-lint + cross-build, Docker `scratch`, services
  systemd/LaunchAgent générés).

### Améliorations mineures

- **🟡 `ProcessInfo.User` — commentaire trompeur** : documenté « vide si instances
  de comptes différents » (`sysinfo.go:66`), mais `aggregateProcesses` renseigne
  toujours le propriétaire de la racine (`userOf[root]`, `sysinfo.go:727`), même
  pour un sous-arbre hétérogène (ce que vérifie d'ailleurs le test « killable »).
  Le code semble correct → **corriger le commentaire**.
- **🟡 Méthode HTTP non contrainte** sur les endpoints GET (`server.go:65-72`) :
  seul `/kill` vérifie `POST`. Utiliser les patterns `mux` Go 1.22+
  (`"GET /api/system"`) pour renvoyer `405` automatiquement.
- **🟡 `Connection: keep-alive` posé à la main** (`server.go:225`) : inutile en
  HTTP/1.1, illégal en HTTP/2. Inoffensif ici (pas de TLS), à retirer.
- **🟡 Tooltip disque spécifique macOS affiché partout** (`app.js:558`) : le texte
  « espace purgeable / Time Machine » s'affiche quel que soit l'OS ; le
  conditionner à `host.platform`.
- **SSE sans heartbeat** : avec un `-r` grand et un proxy à timeout court
  intercalé, la connexion peut tomber entre deux événements ; un commentaire
  `: ping\n\n` périodique règlerait le cas (non critique en localhost direct).

---

## 5. Autres points

- **`dist/go-system-info`** présent localement mais **non suivi par git**
  (`.gitignore` : `/dist/`). Artefact de build — RAS.
- **Dépendances** : `gopsutil/v4 v4.26.5` récent, arbre d'indirects minimal,
  `CGO_ENABLED=0` → binaire statique. Sain.
- **`Collect()` (fonction libre)** : mesure CPU bloquante de 500 ms, bien
  documentée comme réservée au relevé ponctuel, non utilisée par le serveur. OK.
- **Unités** en base 1000 (Go décimaux) back et front : assumé et cohérent.

---

## Recommandations priorisées

1. **Valider `-r` / `-p` au démarrage** (§1.1) — supprime une panique
   déclenchable par simple configuration.
2. **Corriger le CSRF sur `/kill`** (§2.1) — vérifier `Content-Type` / `Origin` ;
   c'est l'action destructrice du service.
3. **Lier les requêtes au contexte de signal** (§1.3) — arrêt gracieux propre,
   plus de faux code d'erreur.
4. **Tolérer les erreurs système transitoires dans le SSE** (§1.4) et **mettre en
   cache les métadonnées statiques** (§3.1).
5. **Rendre le chemin disque dépendant de l'OS et configurable** (§1.2) —
   affichage trompeur sous Windows, volume non choisissable sur Unix.
6. Finitions : injection `innerHTML` (§2.2), corps `/kill` borné (§2.3), flag
   `-host` (§2.4), bugs front (§1.5–1.7), puis §4.
