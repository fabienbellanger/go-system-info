# Audit du projet go-system-info

> Audit statique réalisé le 5 juillet 2026 sur la branche `main` (commit `ac248c3`).
> Périmètre : ajout éventuel de fonctionnalités, qualité de code, architecture, sécurité.

> **Mise à jour du 5 juillet 2026** — les trois correctifs de sécurité prioritaires
> (§1.1, §1.2, §1.3) ont été **implémentés et vérifiés**. Voir la section
> [« Correctifs appliqués »](#6-correctifs-appliqués) en fin de document ; les
> sujets concernés sont annotés ✅ ci-dessous.

## Verdict global

**Projet de très bonne facture.** Architecture en trois couches réellement
découplée, commentaires qui expliquent le *pourquoi* (rare), pièges plateformes
documentés et testés, `go test -race` et `go vet` verts, `govulncheck` sans
vulnérabilité, dépendances à jour (gopsutil v4.26.5, Go 1.26). Le durcissement du
commit `ac248c3` a déjà traité l'essentiel des fautes classiques (validation des
flags, CSRF, timeouts, arrêt gracieux).

Ce document va donc chercher le cran d'après : trois vrais sujets de sécurité,
quelques fragilités mineures, et des pistes de fonctionnalités.

### Vérifications effectuées

| Contrôle | Résultat |
| --- | --- |
| `go test ./... -race` | ✅ vert (3 paquets) |
| `go vet ./...` | ✅ vert |
| `govulncheck ./...` | ✅ aucune vulnérabilité |
| Dépendances (`go.mod`) | ✅ à jour (gopsutil v4.26.5, Go 1.26) |

---

## 1. Sécurité — les trois vrais sujets

### 1.1 Endpoint de kill sans authentification, écoute par défaut sur toutes les interfaces *(point majeur)* ✅ Corrigé

Par défaut le serveur écoute sur `0.0.0.0` et `POST /api/processes/kill` n'exige
aucune authentification. Les gardes existants (propriétaire du processus,
anti-CSRF) protègent contre un *navigateur* tiers, mais **pas** contre un simple
`curl` depuis n'importe quelle machine du réseau local : quiconque partage votre
Wi-Fi peut terminer tous vos processus (session graphique comprise, puisque le
service tourne sous votre compte). Le README recommande `-host 127.0.0.1` mais le
défaut reste ouvert.

**Recommandations**, par ordre de préférence :

- **Mode lecture seule** : un flag `-readonly` qui n'enregistre pas les routes
  `kill`/`detail` — trivial à faire dans `Handler()` (`server.go:75`) et qui rend
  le défaut « toutes interfaces » défendable.
- **Jeton d'accès** : flag `-token` (ou variable d'environnement) exigé en header
  sur les routes mutatives, à défaut d'une vraie authentification.
- Ou **inverser le défaut** : écouter sur `127.0.0.1` sauf `-host 0.0.0.0`
  explicite.

> **Correctif retenu** : flag **`-readonly`** qui neutralise la terminaison
> (`POST /api/processes/kill` répond `403`, route non montée) et masque les
> boutons côté front. Rend défendable l'écoute par défaut sur toutes les
> interfaces. Cf. §6.

### 1.2 La protection CSRF est contournable par DNS rebinding ✅ Corrigé

Le dispositif de `handleKill` (`server.go:203`) — refus de
`Sec-Fetch-Site: cross-site` + Content-Type JSON obligatoire — est bien pensé,
mais il repose sur le fait que la requête est *cross-site*. Avec un **DNS
rebinding** (la page est servie depuis `http://attaquant.tld:8222`, puis le DNS
re-résout ce domaine vers `127.0.0.1`), le `fetch` devient **same-origin** :
`Sec-Fetch-Site: same-origin`, pas de preflight, Content-Type JSON librement
posé. Le kill passe alors depuis une page web, même si le serveur n'écoute que
sur localhost.

**Parade standard** pour un démon local : **valider l'en-tête `Host`**
(n'accepter que `localhost`, `127.0.0.1`, le hostname de la machine — avec le
port attendu) via un petit middleware. C'est le trou résiduel le plus concret du
dispositif actuel.

> **Correctif retenu** : middleware `checkHost` qui n'accepte que `localhost`, le
> nom de la machine (+ `.local`), les adresses IP littérales (non usurpables par
> rebinding) et les noms passés via `-trusted-host` ; tout autre nom reçoit un
> `403`. Cf. §6.

### 1.3 `/api/processes/detail` divulgue la cmdline de n'importe quel PID ✅ Corrigé

Contrairement au kill, `processDetails` (`sysinfo.go:333`) n'applique **aucune
restriction de propriétaire** : n'importe quel client peut demander le détail de
n'importe quel PID et récupérer sa ligne de commande complète. Or les cmdline
contiennent régulièrement des secrets (`mysql -pMotDePasse`, tokens en
argument…), y compris ceux d'autres utilisateurs de la machine.

**À faire** : restreindre au même utilisateur que le serveur (cohérent avec le
garde-fou du kill), ou aux PID effectivement présents dans les groupes du top
courant.

> **Correctif retenu** : `processDetails` filtre désormais par propriétaire (les
> processus d'un autre utilisateur sont ignorés) et ne renvoie rien si
> l'utilisateur du serveur est indéterminable — exactement le garde-fou du kill.
> Cf. §6.

### Durcissements secondaires

- **En-têtes de sécurité absents** : un middleware ajoutant
  `Content-Security-Policy: default-src 'self'`, `X-Content-Type-Options: nosniff`
  et `Referrer-Policy: no-referrer` coûte dix lignes et fournit une défense en
  profondeur.
- **`innerHTML` avec données système** : `updateGauge` (`app.js:41`) insère
  `cpu.model_name` et `disk.path` via `innerHTML`, alors que le projet s'est donné
  la règle « tout ce qui vient du système passe par `textContent` » (et l'applique
  partout ailleurs). Risque quasi nul (valeurs contrôlées par l'opérateur/le
  matériel), mais l'incohérence mérite d'être résorbée pour que la règle tienne.
- **`handleSystem` renvoie `err.Error()` brut en 500** (`server.go:167`) :
  divulgation mineure de chemins internes ; un message générique + log serveur
  suffirait.
- **Pas de journal d'audit des terminaisons** : `killOwnedProcess` réussit
  silencieusement. Un `slog.Info` sur chaque kill accepté serait précieux pour
  tracer.

---

## 2. Qualité de code & correction

Le code est propre ; les remarques sont fines. Les items **actionnables** ont été
traités (cf. §6) ; les autres sont laissés sciemment, avec justification.

- **Faux 0 % réseau au démarrage** *(léger)* ✅ **Corrigé** : dans `netSampler.run`,
  le tout premier `set` ne se produisait qu'au bout d'une seconde, si bien que
  `get()` renvoyait un `Net{}` zéro entre-temps. Le sampler publie désormais les
  volumes cumulés dès le premier relevé (débit encore à 0 jusqu'au second).
- **`truncate` renvoyait un sous-slice du tableau d'origine** ✅ **Corrigé** :
  `procs[:n]` partageait le backing array. Remplacé par `slices.Clip(procs[:n])`,
  qui borne la capacité et documente que le classement renvoyé est final.
- **`collectCPU` rappelle `cpu.Info()` à chaque relevé sur le chemin `Collect()`
  libre** — un syscall pour une valeur constante. **Laissé** : le `Collector`
  (chemin chaud du serveur) met déjà ces métadonnées en cache ; la fonction libre
  `Collect()` n'est qu'un utilitaire de relevé ponctuel, hors chemin serveur.
- **`readProcs` : coût d'énumération** — pour chaque processus, `Name()`,
  `Times()`, `MemoryInfo()`, `Ppid()`, `Uids()` sont autant de lectures `/proc`
  (ou syscalls). **Laissé** : à 3 s d'intervalle c'est absorbé ; c'est un coût
  intrinsèque à l'énumération complète, à garder en tête sur une machine à
  plusieurs milliers de processus.
- **Cache uid→nom jamais invalidé** (`usernameCache`) : les uid recyclés (rare,
  longue session) garderaient un ancien nom. **Laissé** : négligeable en pratique.

**Couverture de tests** : excellente sur `sysinfo` (agrégation, EMA, débit,
anneau) et sur le routage `server`. Les deux angles morts identifiés sont
désormais **comblés** (cf. §6) :

- ✅ `handleStream` en cas d'erreur de collecte (retour anticipé, flux vide).
- ✅ `rootAncestor` sur un cycle de ppid (le garde-fou des 64 itérations).

---

## 3. Architecture

Rien à redire sur le fond : l'injection de `systemCollector` derrière une
interface est le bon choix et paie dans les tests. Trois observations d'échelle,
**pas de défauts** — dont deux sont **conditionnelles** (leur condition
d'activation n'est pas remplie aujourd'hui) et sont donc laissées sciemment, la
troisième (concrète et non conditionnelle) a été corrigée.

- **`Info` mélange métadonnées statiques et dynamiques** dans un seul struct
  sérialisé à chaque tick. Le SSE renvoie donc `host`, `cpu.model_name`,
  `go_version` (invariants) à chaque événement. **Laissé** : la condition (« si le
  débit devenait un sujet ») n'est pas remplie — usage local mono-client, ~200 o
  d'invariants par tick. Séparer en événements `meta`/`tick` casserait le contrat
  de `/api/system` et complexifierait le front pour un gain négligeable. À noter :
  côté **calcul**, ces invariants sont déjà mis en cache par le `Collector` (aucun
  re-syscall) ; seule la sérialisation JSON est répétée sur le fil.
- **`lastState` global + variables de module dans `app.js`** : l'état front est un
  ensemble de globales (`selectedProc`, `loadedInstancesKey`, `instancesGen`…).
  **Laissé** : condition non remplie (« si une 2ᵉ vue arrive »), logique subtile et
  bien commentée, et **absence de tests front** en CI — refactorer du code non
  couvert pour zéro bénéfice fonctionnel serait un mauvais pari.
- **`defaultStreamRefresh` / `minRefreshInterval` dupliquent une logique de
  repli** entre `main.go` et `server.go` ✅ **Corrigé** : la valeur `3s` était
  déclarée deux fois. Unifiée en une constante exportée **`server.DefaultRefresh`**,
  source de vérité unique réutilisée par le défaut du flag `-r` (`main`) et par le
  repli défensif du flux SSE (`handleStream`). `minRefreshInterval` (borne de
  validation CLI, propre à `main`) n'était pas dupliquée : laissée telle quelle.

---

## 4. Fonctionnalités — pistes

Par rapport valeur/effort décroissant. Les quatre premières sont **implémentées**
(cf. §6) ; les deux dernières restent des pistes ouvertes.

1. **Seuils d'alerte visuels + titre d'onglet dynamique** ✅ : le titre de l'onglet
   reflète CPU/RAM et se préfixe d'un ⚠️ au-delà du seuil critique. Seuils nommés
   (`WARN_PCT`/`CRIT_PCT`) partagés par les jauges et l'alerte.
2. **Historique par cœur / température** ✅ : grille d'occupation par cœur
   (`cpu.Times(true)`) et température du capteur le plus chaud
   (`sensors.SensorsTemperatures`, best-effort) dans la carte CPU.
3. **Sélection du volume disque côté UI** ✅ : le serveur énumère les volumes
   montés (`disk.Partitions`), l'interface propose un sélecteur alimenté par le flux.
4. **Filtre/recherche dans la liste de processus et tri par colonne** ✅ : champ de
   recherche par nom + tri (nom / valeur, croissant/décroissant), côté client.
5. **Export Prometheus** (`/metrics`) : le projet collecte déjà tout ; un endpoint
   au format Prometheus le rendrait scrappable par un monitoring existant. Bon
   rapport valeur/effort pour un outil de supervision. *(Ouvert.)*
6. **Top I/O disque par processus** (`process.IOCounters()`) : compléter le
   classement CPU/mémoire par un tri I/O. *(Ouvert.)*

---

## 5. Priorisation

Les trois sujets ci-dessous étaient prioritaires ; ils sont désormais **traités**
(cf. §6). Ce n'étaient pas des bugs de disponibilité — le service tournait
correctement — mais des questions de surface d'exposition.

1. **Validation de l'en-tête `Host`** (§1.2) — ferme le contournement DNS
   rebinding, ~15 lignes de middleware. ✅
2. **Restreindre `/api/processes/detail` au propriétaire** (§1.3) — fuite de
   secrets en cmdline, garde-fou déjà écrit pour le kill à répliquer. ✅
3. **Flag `-readonly`** (§1.1) — rend le défaut « toutes interfaces » défendable. ✅

Les parties §2 (qualité), §3 (architecture) et §4 (fonctionnalités) ont ensuite
été traitées à leur tour (cf. §6). Restent ouverts (non bloquants) : les
**durcissements secondaires** (en-têtes CSP, `innerHTML` sur
`model_name`/`disk.path`, message d'erreur 500 générique, journal d'audit des
kills) et deux **pistes de fonctionnalités** non retenues cette fois — export
Prometheus et top I/O par processus (§4).

---

## 6. Correctifs appliqués

> Implémentés et vérifiés le 5 juillet 2026. `go test ./... -race`, `go vet` et
> `gofmt` restent verts ; comportement confirmé en exécutant le binaire.

### §1.2 — Validation de l'en-tête `Host` (anti DNS rebinding)

- Middleware `checkHost` (`server.go`) enveloppant tout le routage : refuse
  (`403`) tout en-tête `Host` qui n'est ni une **adresse IP littérale** (IPv4/IPv6,
  non usurpables par rebinding) ni un nom de confiance.
- Ensemble des noms de confiance calculé une fois par `New` via `allowedHosts` :
  `localhost`, le nom de la machine (`os.Hostname()`, + variante `.local`),
  l'hôte d'écoute explicite (`-host`) et les noms fournis par le nouveau flag
  **`-trusted-host`** (liste séparée par des virgules, pour un reverse proxy).
- Vérification désactivée quand aucun hôte n'est résolu (`Server` construit sans
  `New`, cas des tests unitaires à construction directe).
- Tests : `TestHostHeaderCheck` (localhost / IP littérale / IPv6 acceptés ;
  `evil.example.com`, `attaquant.tld` refusés).

### §1.3 — `/api/processes/detail` restreint au propriétaire

- `processDetails` prend désormais `currentUser` en paramètre : le propriétaire de
  chaque PID est relevé **avant** toute exposition, et les processus d'un autre
  utilisateur — comme ceux dont le propriétaire est illisible — sont ignorés.
- Si l'utilisateur du serveur est indéterminable (`""`), aucun détail n'est
  renvoyé (même posture que `killOwnedProcess`).
- Test : `TestProcessDetailsOwnerFilter` (le processus de test remonte pour son
  propriétaire, est filtré pour un autre utilisateur, et rien n'est renvoyé sans
  utilisateur de référence). Vérifié aussi à l'exécution : le PID 1 (root) est
  filtré, le PID du serveur (utilisateur courant) remonte.

### §1.1 — Flag `-readonly`

- `Config.ReadOnly` + flag `-readonly` : en lecture seule, `Handler` ne monte pas
  la terminaison réelle et répond `403` (« mode lecture seule »).
- `/api/config` expose désormais `readonly` ; le front (`app.js`) lit ce champ et
  **masque les boutons de terminaison** (application entière et nœuds de l'arbre).
- Tests : `TestReadOnlyDisablesKill` (POST kill → `403`, aucun PID transmis au
  collecteur) et vérification du champ `readonly` dans `TestHandleConfig`.

### Impact sur l'API

| Élément | Avant | Après |
| --- | --- | --- |
| En-tête `Host` inconnu | accepté | `403` (sauf IP / nom de confiance) |
| `GET /api/processes/detail` | tout PID | PID de l'utilisateur du serveur uniquement |
| `POST /api/processes/kill` avec `-readonly` | termine | `403`, route non montée |
| `GET /api/config` | `{refresh_ms}` | `{refresh_ms, readonly}` |
| Nouveaux flags | — | `-readonly`, `-trusted-host` |

### §2 — Qualité de code & couverture de tests

> Implémentés et vérifiés le 5 juillet 2026, dans la continuité des correctifs de
> sécurité. `go test ./... -race`, `go vet` et `gofmt` restent verts.

- **Totaux réseau exposés dès le démarrage** (`netSampler.run`) : publication des
  volumes cumulés au premier relevé, sans attendre le premier intervalle.
  L'affichage ne démarre plus sur des compteurs à zéro. Vérifié à l'exécution : la
  toute première requête `/api/system` renvoie des totaux réels (débit encore à 0,
  ce qui est correct tant que la première paire de relevés n'a pas eu lieu).
- **`truncate` → `slices.Clip`** : capacité bornée, intention explicite. Aucun
  changement de comportement (les classements ne sont pas mutés en aval).
- **Test `TestHandleStreamCollectError`** (`server`) : un collecteur en erreur fait
  se terminer `handleStream` immédiatement ; le client reçoit un `200` puis un flux
  vide (EOF), sans événement `data:`.
- **Test `TestRootAncestorCycle`** (`sysinfo`) : cycle à deux nœuds et auto-parent
  (`pid == ppid`) — la remontée termine via le garde-fou des 64 itérations au lieu
  de boucler.

Items de §2 **laissés sciemment** (justifiés dans la section 2) : rappel de
`cpu.Info()` sur le chemin `Collect()` libre (hors chemin serveur), coût
d'énumération de `readProcs` (intrinsèque), non-invalidation du cache uid→nom
(négligeable).

### §3 — Architecture

> Implémenté et vérifié le 5 juillet 2026. `go test ./... -race`, `go vet` et
> `gofmt` restent verts.

- **Constante `3s` unifiée** : `server.DefaultRefresh` (exportée) remplace la paire
  `main.defaultRefreshInterval` / `server.defaultStreamRefresh`. Source de vérité
  unique, réutilisée par le défaut du flag `-r` et par le repli défensif du flux
  SSE. Vérifié à l'exécution : démarrage par défaut à `refresh=3s`,
  `/api/config` renvoie `refresh_ms: 3000`.
- **Test `TestHandleStreamZeroRefreshFallback`** : un `Server` construit avec
  `Refresh: 0` (hors `parseFlags`) retombe sur `DefaultRefresh` et émet bien le
  premier événement, sans panique de `NewTicker(0)` — garde-fou contre une
  régression future de cette branche.

Items de §3 **laissés sciemment** (conditions d'échelle non remplies, justifiées
dans la section 3) : séparation `meta`/`tick` du payload SSE (gain de bande
passante négligeable en usage local, casserait le contrat d'API) et extraction
d'un module d'état côté `app.js` (aucune 2ᵉ vue, pas de tests front).

### §4 — Fonctionnalités

> Implémenté et vérifié le 5 juillet 2026. Tout passe par le flux SSE existant
> (aucun nouvel endpoint). `go test ./... -race`, `go vet`, `gofmt`,
> `node --check` et `make build-all` (dont le build statique `CGO_ENABLED=0`)
> sont verts.

- **Seuils d'alerte + titre d'onglet dynamique** (`app.js`) : `document.title`
  affiche `CPU X % · RAM Y %`, préfixé d'un ⚠️ dès qu'une des deux franchit le
  seuil critique. Seuils nommés `WARN_PCT`/`CRIT_PCT` partagés par `colorFor` et
  l'alerte.
- **CPU par cœur + température** : `coreSampler` (relevé `cpu.Times(true)`, même
  logique « fantôme » que la jauge globale, mais sans lissage) alimente
  `cpu.per_core` ; `tempSampler` lit `sensors.SensorsTemperatures` (best-effort,
  la goroutine s'arrête si aucun capteur n'est exposé) et publie le capteur le
  plus chaud. Sampler CPU par cœur **distinct** du sampler global pour ne pas
  toucher au correctif macOS de ce dernier. Front : grille de barres par cœur +
  badge température (masqué si absent). Vérifié à l'exécution (8 cœurs, ~52 °C).
- **Sélecteur de volume** : `diskSampler` énumère les volumes montés
  (`selectVolumes` : exclut les pseudo-fs, filtre les tout petits volumes,
  déduplique par occupation brute, garantit le volume par défaut) → `Info.Disks`.
  Front : `<select>` alimenté par le flux, masqué s'il n'y a qu'un volume. Vérifié
  sur macOS (volumes APFS fusionnés en une entrée).
- **Filtre + tri des processus** (`app.js`) : recherche par nom/utilisateur et tri
  (nom / valeur, croissant/décroissant) appliqués côté client sur les classements
  reçus, sans requête supplémentaire. La barre d'outils est hors de la liste
  re-rendue à chaque tick, donc la saisie n'est pas interrompue.
- **Tests** : `TestPerCoreBusy` (delta par cœur + report d'un cœur figé),
  `TestHottestTemp` (max + filtrage des valeurs aberrantes), `TestSelectVolumes`
  (filtrage, dédup, tri, volume par défaut toujours présent). Le filtrage disque a
  été extrait dans `selectVolumes(parts, usage, diskPath)` — l'accès disque est
  injecté pour tester sans dépendre de la machine.

Portée assumée : la recherche de processus s'applique aux **classements reçus**
(top consommateurs), pas à l'ensemble des processus ; la température est un relevé
**best-effort** (souvent absent selon la plateforme/les droits/un binaire sans
cgo). Non retenus dans cette itération : export Prometheus et top I/O par processus
(cf. §4, pistes ouvertes).
