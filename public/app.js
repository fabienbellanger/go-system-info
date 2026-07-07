const DEFAULT_REFRESH_MS = 3000; // valeur de repli si /api/config est indisponible
const CIRCUMFERENCE = 2 * Math.PI * 60; // r = 60

// Seuils d'utilisation (%) : pilotent la couleur des jauges/barres (vert → orange
// → rouge) et l'alerte affichée dans le titre d'onglet au-delà du seuil critique.
const WARN_PCT = 70; // charge notable → orange
const CRIT_PCT = 90; // alerte → rouge

// Titre d'onglet de base, préfixé dynamiquement des mesures CPU/RAM courantes.
const BASE_TITLE = "Informations système";

// Mode d'affichage de la carte Processus : tri par CPU ou par mémoire. Le
// basculement se fait côté client (les deux classements sont déjà reçus), sans
// rouvrir le flux SSE.
let procMode = "cpu";
// Dernier état reçu, mémorisé pour re-rendre les processus lors d'un changement
// de mode sans attendre le prochain événement SSE.
let lastState = null;
// Processus sélectionné (suivi par nom, stable même quand la liste se réordonne)
// et clé des PID déjà chargés dans le panneau de détails (pour éviter de
// re-télécharger les instances tant qu'elles n'ont pas changé).
let selectedProc = null;
let selectedPids = [];
let selectedKillable = false;
let loadedInstancesKey = null; // clé des PID chargés avec succès dans le panneau
let loadingInstancesKey = null; // clé en cours de chargement (évite les requêtes redondantes à chaque tick)
let instancesGen = 0; // génération courante : ignore les réponses d'une requête devenue obsolète
// Mode lecture seule : renseigné depuis /api/config. Quand il est actif, le
// serveur refuse la terminaison ; le front masque donc les boutons associés.
let readOnly = false;
// Volume disque affiché, suivi par chemin (null = volume par défaut du flux).
let selectedDisk = null;
let diskOptionsKey = null; // clé des options actuellement peuplées dans le <select>
// Filtre et tri de la liste de processus, appliqués côté client sur les
// classements déjà reçus (le serveur envoie le top des consommateurs).
let procFilter = "";
const procSort = { key: "value", dir: "desc" }; // key : "value" | "name"

// Couleur selon le seuil d'utilisation.
function colorFor(pct) {
  if (pct >= CRIT_PCT) return "var(--red)";
  if (pct >= WARN_PCT) return "var(--orange)";
  return "var(--green)";
}

function updateGauge(prefix, pct, detail, sub) {
  const value = document.getElementById(`${prefix}-value`);
  const arc = document.getElementById(`${prefix}-arc`);
  const det = document.getElementById(`${prefix}-detail`);
  const color = colorFor(pct);

  value.textContent = pct.toFixed(0);
  arc.style.stroke = color;
  arc.style.strokeDashoffset = CIRCUMFERENCE * (1 - Math.min(pct, 100) / 100);
  // Halo coloré porté par le conteneur (box-shadow via --gauge-glow), au lieu
  // d'un drop-shadow SVG recalculé à chaque frame de la transition de l'arc.
  const gauge = arc.closest(".gauge");
  if (gauge) gauge.style.setProperty("--gauge-glow", color);
  det.innerHTML = detail + (sub ? `<span class="sub">${sub}</span>` : "");
}

// renderSparkline trace la courbe d'évolution d'une série de pourcentages
// (0–100) dans le SVG du préfixe donné. La couleur suit le seuil de la
// dernière valeur, en cohérence avec la jauge.
function renderSparkline(prefix, values) {
  const line = document.getElementById(`${prefix}-spark-line`);
  const area = document.getElementById(`${prefix}-spark-area`);
  const svg = document.getElementById(`${prefix}-spark`);
  if (!line || !area || !svg || values.length === 0) return;

  const W = 100;
  const H = 28;
  const stepX = values.length > 1 ? W / (values.length - 1) : 0;
  const coords = values.map((v, i) => {
    const x = i * stepX;
    const y = H - (Math.min(Math.max(v, 0), 100) / 100) * H;
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  });

  line.setAttribute("points", coords.join(" "));
  area.setAttribute("points", `0,${H} ${coords.join(" ")} ${W},${H}`);
  svg.style.color = colorFor(values[values.length - 1]);
}

// renderCores affiche une grille de barres verticales, une par cœur logique,
// chacune colorée selon le seuil. Vide si le relevé par cœur n'est pas disponible.
function renderCores(perCore) {
  const box = document.getElementById("cpu-cores");
  if (!box) return;
  if (!Array.isArray(perCore) || perCore.length === 0) {
    box.replaceChildren();
    return;
  }
  box.replaceChildren(
    ...perCore.map((pct, i) => {
      const v = Math.min(Math.max(pct, 0), 100);
      const cell = document.createElement("div");
      cell.className = "cpu-core";
      cell.title = `Cœur ${i} : ${v.toFixed(0)} %`;
      const fill = document.createElement("span");
      fill.className = "cpu-core-fill";
      fill.style.height = `${v}%`;
      fill.style.background = colorFor(v);
      cell.appendChild(fill);
      return cell;
    }),
  );
}

// renderTemp affiche la température du capteur le plus chaud, si disponible
// (souvent absente selon la plateforme et les droits — le badge reste alors masqué).
function renderTemp(cpu) {
  const el = document.getElementById("cpu-temp");
  if (!el) return;
  const t = cpu.temp_celsius || 0;
  if (t > 0) {
    el.textContent = `🌡️ ${t.toFixed(0)} °C`;
    el.title = cpu.temp_label ? `Capteur le plus chaud : ${cpu.temp_label}` : "Capteur le plus chaud";
    el.hidden = false;
  } else {
    el.hidden = true;
  }
}

// renderSwap affiche l'occupation du swap (mémoire d'échange) sous forme d'un
// libellé et d'une barre colorée selon le seuil. Quand aucun swap n'est actif
// (total nul), l'état « inactif » est affiché — une information en soi.
function renderSwap(memory) {
  const val = document.getElementById("mem-swap-val");
  const fill = document.getElementById("mem-swap-fill");
  if (!val || !fill) return;
  const total = memory.swap_total_gb || 0;
  if (total <= 0) {
    val.textContent = "inactif";
    fill.style.width = "0%";
    return;
  }
  const pct = Math.min(Math.max(memory.swap_used_percent || 0, 0), 100);
  val.textContent = `${(memory.swap_used_gb || 0).toFixed(1)} / ${total.toFixed(1)} Go`;
  fill.style.width = `${pct}%`;
  fill.style.background = colorFor(pct);
}

// updateTitle reflète les mesures CPU/RAM dans le titre de l'onglet (utile quand
// il est en arrière-plan) et le préfixe d'un ⚠️ dès qu'une des deux franchit le
// seuil critique.
function updateTitle(cpuPct, memPct) {
  const alert = cpuPct >= CRIT_PCT || memPct >= CRIT_PCT;
  const prefix = alert ? "⚠️ " : "";
  document.title = `${prefix}CPU ${Math.round(cpuPct)} % · RAM ${Math.round(memPct)} % — ${BASE_TITLE}`;
}

// renderProcesses remplit la liste des processus selon le mode courant
// (CPU ou mémoire), puis synchronise le panneau de détails. `processes` provient
// du flux ({ top_cpu, top_mem }) ; il peut être absent/null tant que le premier
// échantillon n'a pas été calculé.
function renderProcesses(processes) {
  const list = document.getElementById("proc-list");
  if (!list) return;

  const base = processes ? (procMode === "mem" ? processes.top_mem : processes.top_cpu) : null;
  if (!Array.isArray(base) || base.length === 0) {
    list.replaceChildren(procEmpty(processes ? "Aucune donnée de processus" : "Mesure en cours…"));
  } else {
    const items = sortProcs(filterProcs(base));
    if (items.length === 0) {
      list.replaceChildren(procEmpty("Aucun processus ne correspond au filtre"));
    } else {
      list.replaceChildren(...items.map(buildProcRow));
    }
  }

  syncProcDetail(processes);
}

// procEmpty construit une ligne d'état (liste vide, filtre sans résultat…).
function procEmpty(text) {
  const li = document.createElement("li");
  li.className = "proc-empty";
  li.textContent = text;
  return li;
}

// filterProcs ne conserve que les processus dont le nom ou l'utilisateur contient
// le filtre courant (recherche insensible à la casse). Le filtre s'applique aux
// classements reçus (top consommateurs) — pas à l'ensemble des processus.
function filterProcs(items) {
  const q = procFilter.trim().toLowerCase();
  if (!q) return items;
  return items.filter((p) => (p.name || "").toLowerCase().includes(q) || (p.user || "").toLowerCase().includes(q));
}

// sortProcs renvoie une copie triée selon procSort. La valeur comparée suit le
// mode courant (mémoire → octets ; CPU → % d'un cœur) ; « name » trie par nom.
function sortProcs(items) {
  const value = (p) => (procMode === "mem" ? p.mem_bytes || 0 : p.cpu_percent || 0);
  const copy = items.slice();
  copy.sort((a, b) => {
    const cmp =
      procSort.key === "name"
        ? (a.name || "").localeCompare(b.name || "", "fr", { sensitivity: "base" })
        : value(a) - value(b);
    return procSort.dir === "asc" ? cmp : -cmp;
  });
  return copy;
}

// buildProcRow construit la ligne d'un processus (rang via CSS, nom, utilisateur,
// barre, valeur). Cliquer la ligne la sélectionne (suivi par nom). Les chaînes
// issues du système sont insérées via textContent.
function buildProcRow(p) {
  const li = document.createElement("li");
  li.className = "proc-row";
  if (p.name === selectedProc) li.classList.add("selected");
  li.tabIndex = 0;
  li.addEventListener("click", () => selectProc(p.name));
  li.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      selectProc(p.name);
    }
  });

  const name = document.createElement("span");
  name.className = "proc-name";
  name.textContent = p.name || "—";
  name.title = p.name || "";
  if (p.count > 1) {
    const count = document.createElement("span");
    count.className = "proc-count";
    count.textContent = `×${p.count}`;
    name.appendChild(count);
  }

  const user = document.createElement("span");
  user.className = "proc-user";
  user.textContent = p.user || "—";
  user.title = p.user ? `Lancé par ${p.user}` : "Propriétaire indéterminé";

  // Le pourcentage pilote la barre et la couleur. En mode CPU il s'agit du %
  // d'un cœur (style top, peut dépasser 100 %) ; la barre est plafonnée à 100 %.
  const pct = procMode === "mem" ? p.mem_percent || 0 : p.cpu_percent || 0;
  const color = colorFor(pct);

  const bar = document.createElement("span");
  bar.className = "proc-bar";
  const fill = document.createElement("span");
  fill.className = "proc-bar-fill";
  fill.style.width = `${Math.min(Math.max(pct, 0), 100)}%`;
  fill.style.background = color;
  bar.appendChild(fill);

  const val = document.createElement("span");
  val.className = "proc-val";
  val.style.color = color;
  if (procMode === "mem") {
    val.textContent = formatBytes(p.mem_bytes || 0);
  } else {
    // CPU : valeur principale en % d'un cœur (style top), et en dessous, plus
    // discret, la même charge rapportée à la machine entière (% machine), sur
    // la même base que la jauge CPU globale.
    const main = document.createElement("span");
    main.className = "proc-val-main";
    main.textContent = formatCpu(p.cpu_percent || 0);
    const sub = document.createElement("span");
    sub.className = "proc-val-sub";
    sub.textContent = formatCpu(p.cpu_percent_system || 0);
    val.title = `${formatCpu(p.cpu_percent || 0)} d'un cœur · ${formatCpu(p.cpu_percent_system || 0)} de la machine`;
    val.append(main, sub);
  }

  li.append(name, user, bar, val);
  return li;
}

// formatCpu met en forme un pourcentage CPU par cœur (style top) : une décimale
// en dessous de 10 % pour ne pas perdre les petites valeurs, sinon un entier.
function formatCpu(pct) {
  const v = Math.max(pct, 0);
  return `${v.toFixed(v < 10 ? 1 : 0)} %`;
}

// findProcByName retrouve un groupe par nom dans les deux classements.
function findProcByName(processes, name) {
  if (!processes) return null;
  const lists = [processes.top_cpu, processes.top_mem];
  for (const list of lists) {
    if (Array.isArray(list)) {
      const hit = list.find((p) => p.name === name);
      if (hit) return hit;
    }
  }
  return null;
}

// selectProc (dé)sélectionne un groupe de processus et rafraîchit l'affichage.
function selectProc(name) {
  selectedProc = selectedProc === name ? null : name;
  loadedInstancesKey = null; // forcera le rechargement des instances
  if (lastState) renderProcesses(lastState.system.processes);
}

// syncProcDetail met à jour le panneau de détails (latéral) à partir des données
// vivantes. Le résumé suit chaque rafraîchissement ; l'arbre des processus n'est
// rechargé que lorsque l'ensemble des PID du groupe change.
function syncProcDetail(processes) {
  const panel = document.getElementById("proc-detail");
  const body = document.getElementById("proc-body");
  if (!panel) return;
  if (!selectedProc) {
    panel.hidden = true;
    if (body) body.classList.remove("with-detail");
    return;
  }
  panel.hidden = false;
  if (body) body.classList.add("with-detail");
  document.getElementById("pd-title").textContent = selectedProc;

  const item = findProcByName(processes, selectedProc);
  const setText = (id, txt) => {
    document.getElementById(id).textContent = txt;
  };

  if (item) {
    selectedPids = Array.isArray(item.pids) ? item.pids : [];
    selectedKillable = !!item.killable;
    setText("pd-user", item.user || "—");
    setText("pd-count", String(item.count || selectedPids.length));
    setText("pd-cpu", `${formatCpu(item.cpu_percent || 0)} cœur · ${formatCpu(item.cpu_percent_system || 0)} machine`);
    setText("pd-mem", `${formatBytes(item.mem_bytes || 0)} · ${(item.mem_percent || 0).toFixed(1)} %`);
  } else {
    // L'application est sortie du top 10 : on garde la sélection mais on
    // signale que le résumé n'est plus rafraîchi.
    selectedKillable = false;
    setText("pd-user", "—");
    setText("pd-count", "—");
    setText("pd-cpu", "hors du top 10");
    setText("pd-mem", "—");
  }

  // Bouton de terminaison de toute l'application : seulement si « killable » et
  // hors mode lecture seule (où le serveur refuserait la terminaison).
  const actions = document.getElementById("pd-actions");
  if (!readOnly && item && item.killable && selectedPids.length > 0) {
    if (!actions.querySelector(".proc-kill")) {
      const kill = document.createElement("button");
      kill.type = "button";
      kill.className = "proc-kill";
      kill.textContent = "Terminer l'application";
      kill.addEventListener("click", killSelected);
      actions.replaceChildren(kill);
    }
  } else {
    actions.replaceChildren();
  }

  // Recharge l'arbre quand l'ensemble des PID a changé — mais pas si ce même
  // ensemble est déjà en cours de chargement (sinon un fetch repartirait à
  // chaque tick tant que la réponse n'est pas arrivée).
  const key = selectedPids.join(",");
  if (key && key !== loadedInstancesKey && key !== loadingInstancesKey) {
    loadInstances(selectedPids, key);
  }
}

// loadInstances récupère le détail par PID du groupe sélectionné et en rend
// l'arbre (parent → enfants). La clé n'est mémorisée qu'en cas de succès (un
// échec pourra donc être retenté), et un jeton de génération écarte la réponse
// d'une requête supplantée par une sélection plus récente.
async function loadInstances(pids, key) {
  const box = document.getElementById("pd-instances");
  if (!box) return;
  const gen = ++instancesGen;
  loadingInstancesKey = key;
  box.textContent = "Chargement de l'arbre…";
  try {
    const res = await fetch(`/api/processes/detail?pids=${pids.join(",")}`, { cache: "no-store" });
    if (gen !== instancesGen) return; // supplantée par une requête plus récente
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    if (gen !== instancesGen) return;
    renderTree(box, data.instances || []);
    loadedInstancesKey = key;
  } catch (err) {
    if (gen !== instancesGen) return;
    loadedInstancesKey = null; // échec : autorise une nouvelle tentative
    box.textContent = `Détails indisponibles : ${err.message}`;
  } finally {
    if (gen === instancesGen) loadingInstancesKey = null;
  }
}

// renderTree reconstruit la hiérarchie parent → enfants à partir des PID/PPID et
// la rend, chaque nœud étant indenté selon sa profondeur.
function renderTree(box, instances) {
  if (!instances.length) {
    box.textContent = "Aucun processus actif.";
    return;
  }
  const byPid = new Map(instances.map((d) => [d.pid, d]));
  const children = new Map();
  const roots = [];
  for (const d of instances) {
    if (byPid.has(d.ppid) && d.ppid !== d.pid) {
      if (!children.has(d.ppid)) children.set(d.ppid, []);
      children.get(d.ppid).push(d);
    } else {
      roots.push(d);
    }
  }
  const byPidAsc = (a, b) => a.pid - b.pid;
  roots.sort(byPidAsc);
  for (const list of children.values()) list.sort(byPidAsc);

  const frag = document.createDocumentFragment();
  const seen = new Set();
  const walk = (d, depth) => {
    if (seen.has(d.pid) || depth > 32) return; // garde-fou anti-cycle
    seen.add(d.pid);
    frag.appendChild(buildTreeNode(d, depth, children));
    for (const c of children.get(d.pid) || []) walk(c, depth + 1);
  };
  for (const r of roots) walk(r, 0);
  box.replaceChildren(frag);
}

// buildTreeNode construit la ligne d'un processus de l'arbre, avec un bouton de
// terminaison (ce processus et ses enfants) lorsque le groupe est terminable.
function buildTreeNode(d, depth, children) {
  const row = document.createElement("div");
  row.className = "pd-node";
  row.style.setProperty("--depth", depth);

  const info = document.createElement("div");
  info.className = "pd-node-info";

  const head = document.createElement("div");
  head.className = "pd-node-head";
  const pid = document.createElement("span");
  pid.className = "pd-pid";
  pid.textContent = d.name ? `${d.name}` : `PID ${d.pid}`;
  pid.title = d.cmdline || "";
  const meta = document.createElement("span");
  meta.className = "pd-meta";
  const started = d.create_time ? `↑ ${formatUptime((Date.now() - d.create_time) / 1000)}` : "";
  meta.textContent = [
    `PID ${d.pid}`,
    formatBytes(d.mem_bytes || 0),
    d.threads ? `${d.threads} thr` : "",
    d.status || "",
    started,
  ]
    .filter(Boolean)
    .join(" · ");
  head.append(pid, meta);
  info.append(head);

  row.append(info);

  if (selectedKillable && !readOnly) {
    const kill = document.createElement("button");
    kill.type = "button";
    kill.className = "pd-node-kill";
    kill.textContent = "✕";
    kill.title = "Terminer ce processus et ses enfants";
    kill.addEventListener("click", (e) => {
      e.stopPropagation();
      killNode(d, children, kill);
    });
    row.append(kill);
  }
  return row;
}

// collectSubtree renvoie le PID d'un nœud et de tous ses descendants.
function collectSubtree(d, children) {
  const pids = [d.pid];
  for (const c of children.get(d.pid) || []) pids.push(...collectSubtree(c, children));
  return pids;
}

// killNode termine un processus de l'arbre et ses descendants (après confirmation).
async function killNode(d, children, btn) {
  const pids = collectSubtree(d, children);
  const extra = pids.length - 1;
  const label =
    extra > 0
      ? `« ${d.name || d.pid} » (PID ${d.pid}) et ses ${extra} enfant(s)`
      : `« ${d.name || d.pid} » (PID ${d.pid})`;
  if (!window.confirm(`Terminer ${label} ?`)) return;

  if (btn) btn.disabled = true;
  try {
    await killPids(pids);
    // Recharge l'arbre tout de suite : les PID disparus sont ignorés. On force
    // le rechargement car l'ensemble des PID (donc la clé) n'a pas changé.
    if (selectedPids.length) {
      loadedInstancesKey = null;
      loadInstances(selectedPids, selectedPids.join(","));
    }
  } catch (err) {
    if (btn) {
      btn.disabled = false;
      btn.title = `Échec : ${err.message}`;
    }
  }
}

// killSelected termine toute l'application sélectionnée (après confirmation),
// puis referme le panneau. La liste se rafraîchit au tick suivant.
async function killSelected() {
  if (!selectedPids.length) return;
  const count = selectedPids.length;
  if (!window.confirm(`Terminer « ${selectedProc} » et ses ${count} processus ?`)) return;

  const btn = document.querySelector("#pd-actions .proc-kill");
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Terminaison…";
  }
  try {
    await killPids(selectedPids);
    selectedProc = null; // referme le panneau ; la liste se met à jour au tick suivant
    if (lastState) renderProcesses(lastState.system.processes);
  } catch (err) {
    if (btn) {
      btn.disabled = false;
      btn.textContent = "Terminer l'application";
      btn.title = `Échec de la terminaison : ${err.message}`;
    }
  }
}

// killPids demande la terminaison d'une liste de PID. Le serveur répond 200 même
// quand des terminaisons échouent (le détail est dans results[]) : on inspecte
// donc les résultats et on lève une erreur si AUCUNE n'a abouti, afin que
// l'appelant ne traite pas un échec total comme un succès.
async function killPids(pids) {
  const res = await fetch("/api/processes/kill", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ pids }),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = await res.json();
  const results = Array.isArray(data.results) ? data.results : [];
  const failures = results.filter((r) => !r.ok);
  if (results.length > 0 && failures.length === results.length) {
    throw new Error(failures[0].error || "terminaison refusée");
  }
  return { results, failures };
}

// setupProcToggle câble les commandes de la carte Processus : fermeture du
// panneau de détails, bascule CPU/Mémoire, recherche et tri. Chaque interaction
// re-rend immédiatement à partir du dernier état reçu.
function setupProcToggle() {
  const close = document.getElementById("pd-close");
  if (close) {
    close.addEventListener("click", () => {
      selectedProc = null;
      if (lastState) renderProcesses(lastState.system.processes);
    });
  }

  const toggle = document.getElementById("proc-toggle");
  if (toggle) {
    toggle.addEventListener("click", (event) => {
      const btn = event.target.closest(".seg-btn");
      if (!btn || btn.classList.contains("active")) return;
      procMode = btn.dataset.mode === "mem" ? "mem" : "cpu";
      toggle.querySelectorAll(".seg-btn").forEach((b) => {
        b.classList.toggle("active", b === btn);
      });
      if (lastState) renderProcesses(lastState.system.processes);
    });
  }

  const search = document.getElementById("proc-search");
  if (search) {
    search.addEventListener("input", () => {
      procFilter = search.value;
      if (lastState) renderProcesses(lastState.system.processes);
    });
  }

  const sortBox = document.getElementById("proc-sort");
  if (sortBox) {
    sortBox.addEventListener("click", (event) => {
      const btn = event.target.closest(".sort-btn");
      if (!btn) return;
      const key = btn.dataset.key === "name" ? "name" : "value";
      if (procSort.key === key) {
        procSort.dir = procSort.dir === "asc" ? "desc" : "asc"; // re-clic : inverse le sens
      } else {
        procSort.key = key;
        procSort.dir = key === "name" ? "asc" : "desc"; // sens par défaut sensé par colonne
      }
      updateSortButtons();
      if (lastState) renderProcesses(lastState.system.processes);
    });
    updateSortButtons();
  }
}

// setupDiskSelect câble le sélecteur de volume : le choix re-rend aussitôt la
// carte Disque à partir du dernier état reçu.
function setupDiskSelect() {
  const sel = document.getElementById("disk-select");
  if (!sel) return;
  sel.addEventListener("change", () => {
    selectedDisk = sel.value;
    if (lastState) updateDiskCard(lastState.system);
  });
}

// formatBytes met en forme un volume d'octets en unités décimales lisibles,
// cohérentes avec l'affichage des Go (base 1000) du reste de l'interface.
function formatBytes(bytes) {
  const units = ["o", "Ko", "Mo", "Go", "To"];
  let v = Math.max(bytes, 0);
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  // Une décimale pour les unités >= Ko tant que la valeur reste petite.
  const digits = i > 0 && v < 100 ? 1 : 0;
  return `${v.toFixed(digits)} ${units[i]}`;
}

// formatRate met en forme un débit (octets/s).
function formatRate(bytesPerSec) {
  return `${formatBytes(bytesPerSec)}/s`;
}

// formatLoad met en forme la charge moyenne 1/5/15 min.
function formatLoad(load) {
  if (!load) return "—";
  return `${load.load1.toFixed(2)} / ${load.load5.toFixed(2)} / ${load.load15.toFixed(2)}`;
}

function formatUptime(seconds) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const parts = [];
  if (d) parts.push(`${d} j`);
  if (h) parts.push(`${h} h`);
  parts.push(`${m} min`);
  return parts.join(" ");
}

// État de connexion ------------------------------------------------------
// Le flux SSE peut être rompu (serveur arrêté, réseau coupé). Les mesures
// affichées sont alors figées : on le signale en désaturant les jauges et les
// valeurs (classe `offline` sur <body>) et via un badge rouge « Hors ligne »
// qui décompte le temps écoulé depuis la rupture.
//
// Une micro-coupure (reconnexion quasi immédiate du flux) ne doit pas faire
// clignoter le badge : on n'allume l'état hors ligne qu'après un délai de
// grâce (markOffline), annulé dès qu'une mesure arrive (goOnline).
let offlineSince = 0; // horodatage (ms) de la rupture du flux
let offlineTimer = null; // délai de grâce avant de basculer hors ligne
let offlineTicker = null; // intervalle de mise à jour du libellé hors ligne

// goOnline : flux actif, données fraîches. Annule tout passage hors ligne,
// qu'il soit en attente du délai de grâce ou déjà affiché.
function goOnline(title) {
  if (offlineTimer) {
    clearTimeout(offlineTimer);
    offlineTimer = null;
  }
  if (offlineTicker) {
    clearInterval(offlineTicker);
    offlineTicker = null;
  }
  offlineSince = 0;
  document.body.classList.remove("offline");
  document.getElementById("status").classList.remove("offline");
  document.getElementById("status-dot").classList.remove("error");
  document.getElementById("status-text").textContent = "";
  document.getElementById("status").title = title;
}

// markOffline programme le passage hors ligne après un délai de grâce
// (graceMs) : tant qu'il n'a pas expiré, l'interface reste « en ligne ». Le
// décompte affiché ensuite part de l'instant réel de la rupture.
function markOffline(graceMs, title) {
  if (offlineTimer || document.body.classList.contains("offline")) return;
  offlineSince = Date.now();
  offlineTimer = setTimeout(() => {
    offlineTimer = null;
    goOffline(title);
  }, graceMs);
}

// goOffline bascule réellement l'interface en mode figé (données gelées).
function goOffline(title) {
  if (!offlineSince) offlineSince = Date.now();
  document.body.classList.add("offline");
  document.getElementById("status").classList.add("offline");
  const dot = document.getElementById("status-dot");
  dot.classList.remove("pulse"); // coupe toute onde en cours avant le clignotement rouge
  dot.classList.add("error");
  document.getElementById("status").title = title;
  if (!offlineTicker) {
    renderOfflineLabel();
    offlineTicker = setInterval(renderOfflineLabel, 1000);
  }
}

// renderOfflineLabel affiche depuis combien de temps le flux est interrompu.
function renderOfflineLabel() {
  const secs = Math.max(0, Math.round((Date.now() - offlineSince) / 1000));
  document.getElementById("status-text").textContent = secs < 3 ? "Hors ligne" : `Hors ligne · ${secs} s`;
}

// pulseStatus émet une onde unique sur le point de statut à chaque mise à jour
// reçue : un repère de vie ponctuel, sans animation continue (qui maintiendrait
// le navigateur en rendu permanent et ferait grimper le CPU au repos).
function pulseStatus() {
  const dot = document.getElementById("status-dot");
  dot.classList.remove("pulse");
  void dot.offsetWidth; // force un reflow pour pouvoir relancer l'animation
  dot.classList.add("pulse");
}

// buildHostRow construit une ligne clé/valeur de la carte Hôte. `value` est soit
// une chaîne (insérée via textContent, donc sûre vis-à-vis des valeurs système),
// soit un nœud déjà construit. `title` alimente l'infobulle éventuelle.
function buildHostRow(key, value, title) {
  const li = document.createElement("li");
  if (title) li.title = title;
  const k = document.createElement("span");
  k.className = "key";
  k.textContent = key;
  const v = document.createElement("span");
  v.className = "val";
  if (value instanceof Node) v.appendChild(value);
  else v.textContent = value;
  li.append(k, v);
  return li;
}

// updateDiskCard met à jour la jauge disque pour le volume choisi (ou le volume
// par défaut du flux) et alimente le sélecteur de volumes.
function updateDiskCard(data) {
  const disks = Array.isArray(data.disks) ? data.disks : [];
  populateDiskSelect(disks, data.disk.path);

  // Volume à afficher : le choix de l'utilisateur s'il est encore monté, sinon
  // le volume par défaut porté par le flux.
  let vol = data.disk;
  if (selectedDisk) {
    const hit = disks.find((d) => d.path === selectedDisk);
    if (hit) vol = hit;
  }

  // La note « purgeable » est propre à macOS (cf. version historique).
  const isDarwin = data.host && data.host.os === "darwin";
  const sub = isDarwin
    ? `Montage ${vol.path}<span class="note" title="Espace purgeable non inclus : les snapshots Time Machine locaux et caches récupérables automatiquement par macOS ne sont pas comptés comme disponibles ici, contrairement au Finder ou à CleanMyMac. La valeur reflète l'espace réellement libre au sens du système de fichiers.">ℹ️</span>`
    : `Montage ${vol.path}`;
  updateGauge(
    "disk",
    vol.used_percent,
    `${(vol.total_gb - vol.used_gb).toFixed(0)} / ${vol.total_gb.toFixed(0)} Go restant`,
    sub,
  );

  // Infos complémentaires : système de fichiers du volume affiché et débit
  // d'E/S disque global (toutes unités), calculé côté serveur comme le réseau.
  const fstypeEl = document.getElementById("disk-fstype");
  if (fstypeEl) fstypeEl.textContent = vol.fstype || data.disk.fstype || "—";
  const io = data.disk_io || {};
  const read = document.getElementById("disk-read");
  const write = document.getElementById("disk-write");
  if (read) read.textContent = formatRate(io.read_bytes_per_sec || 0);
  if (write) write.textContent = formatRate(io.write_bytes_per_sec || 0);
}

// populateDiskSelect (re)construit les options du sélecteur de volumes quand la
// liste change, et le masque quand il n'y a pas de choix (0 ou 1 volume).
function populateDiskSelect(disks, defaultPath) {
  const sel = document.getElementById("disk-select");
  if (!sel) return;
  if (disks.length <= 1) {
    sel.hidden = true;
    diskOptionsKey = null;
    return;
  }
  sel.hidden = false;

  const key = disks.map((d) => d.path).join("|");
  if (key !== diskOptionsKey) {
    diskOptionsKey = key;
    sel.replaceChildren(
      ...disks.map((d) => {
        const opt = document.createElement("option");
        opt.value = d.path;
        opt.textContent = d.path; // chemin système : inséré comme texte
        return opt;
      }),
    );
  }
  // Reflète la sélection courante (retombe sur le défaut si le choix a disparu).
  const current = selectedDisk && disks.some((d) => d.path === selectedDisk) ? selectedDisk : defaultPath;
  if (sel.value !== current) sel.value = current;
  // Filet de sécurité : si aucune option ne correspond (chemin par défaut absent
  // de la liste, cas anormal), on sélectionne la première pour ne jamais laisser
  // le sélecteur affiché sans texte.
  if (sel.selectedIndex < 0 && sel.options.length > 0) sel.selectedIndex = 0;
}

// updateSortButtons synchronise l'état visuel des boutons de tri (actif + flèche
// de direction) avec procSort.
function updateSortButtons() {
  const box = document.getElementById("proc-sort");
  if (!box) return;
  box.querySelectorAll(".sort-btn").forEach((b) => {
    const active = b.dataset.key === procSort.key;
    b.classList.toggle("active", active);
    const label = b.dataset.key === "name" ? "Nom" : "Valeur";
    b.textContent = active ? `${label} ${procSort.dir === "asc" ? "▲" : "▼"}` : label;
  });
}

// applyState met à jour l'interface à partir d'un état poussé par le flux SSE
// ({ system, history }).
function applyState(state) {
  const data = state.system;
  lastState = state; // mémorisé pour le re-rendu des processus au changement de mode

  updateGauge("cpu", data.cpu.used_percent, `${data.cpu.cores} cœurs`, data.cpu.model_name || "CPU");
  renderCores(data.cpu.per_core);
  renderTemp(data.cpu);

  updateGauge(
    "mem",
    data.memory.used_percent,
    `${data.memory.used_gb.toFixed(1)} / ${data.memory.total_gb.toFixed(1)} Go`,
    `${data.memory.free_gb.toFixed(1)} Go libres`,
  );
  renderSwap(data.memory);

  // Titre d'onglet dynamique (CPU/RAM), avec alerte au-delà du seuil critique.
  updateTitle(data.cpu.used_percent, data.memory.used_percent);

  updateDiskCard(data);

  const net = data.net || {};
  document.getElementById("net-recv").textContent = formatRate(net.recv_bytes_per_sec || 0);
  document.getElementById("net-sent").textContent = formatRate(net.sent_bytes_per_sec || 0);
  document.getElementById("net-recv-total").textContent = formatBytes(net.recv_total_bytes || 0);
  document.getElementById("net-sent-total").textContent = formatBytes(net.sent_total_bytes || 0);

  const host = data.host;
  const cores = data.cpu.cores;
  // Charge : le triplet 1/5/15 min, suivi d'un repère « · N cœurs » pour situer
  // la valeur ; une infobulle explique la lecture.
  const loadTitle =
    `Charge système moyenne (load average) sur 1, 5 et 15 min : nombre moyen de processus ` +
    `actifs ou en attente du CPU. À comparer aux ${cores} cœurs — en dessous il reste de la ` +
    `marge, au-dessus le système est surchargé. Ce n'est pas un pourcentage CPU.`;

  let loadValue;
  if (data.load) {
    loadValue = document.createElement("span");
    loadValue.append(formatLoad(data.load)); // texte, puis le repère « · N cœurs »
    const ref = document.createElement("span");
    ref.className = "ref";
    ref.textContent = `· ${cores} cœurs`;
    loadValue.append(ref);
  } else {
    loadValue = "—";
  }

  // Les champs hôte proviennent du système : insérés comme texte (jamais comme
  // HTML), à l'image du reste de l'interface, pour écarter toute injection.
  document
    .getElementById("host-list")
    .replaceChildren(
      buildHostRow("🏠 Nom", host.hostname || "—"),
      buildHostRow("🖥️ Système", `${host.platform || host.os || "—"} / ${host.kernel_arch || "—"}`),
      buildHostRow("📈 Charge", loadValue, loadTitle),
      buildHostRow("⏱️ Uptime", formatUptime(host.uptime_seconds)),
    );

  const hist = state.history;
  if (Array.isArray(hist) && hist.length > 0) {
    renderSparkline(
      "cpu",
      hist.map((s) => s.cpu),
    );
    renderSparkline(
      "mem",
      hist.map((s) => s.mem),
    );
  }

  renderProcesses(data.processes);

  const time = new Date(data.timestamp).toLocaleTimeString("fr-FR");
  goOnline(`À jour · dernière mesure à ${time}`);
  pulseStatus();
}

// connect ouvre le flux SSE et met à jour l'interface à chaque événement.
// EventSource gère la reconnexion automatiquement en cas de coupure.
function connect(intervalMs) {
  // Délai de grâce avant d'annoncer une coupure : ~2 intervalles ratés, avec
  // un plancher pour les rafraîchissements très rapides.
  const graceMs = Math.max(2 * intervalMs, 2000);
  const source = new EventSource("/api/stream");

  source.onmessage = (event) => {
    try {
      applyState(JSON.parse(event.data));
    } catch (err) {
      // Flux vivant mais donnée illisible : signalé sans délai de grâce.
      goOffline(`Données invalides : ${err.message}`);
    }
  };

  source.onerror = () => {
    // Coupure possible : EventSource retentera seul. On n'allume le badge
    // qu'après le délai de grâce, pour ignorer les micro-coupures.
    markOffline(graceMs, "Flux interrompu : tentative de reconnexion…");
  };
}

// Récupère la configuration serveur : l'intervalle de rafraîchissement (flag -r)
// et le mode lecture seule (flag -readonly), ce dernier mémorisé dans `readOnly`
// pour masquer les actions de terminaison. Renvoie l'intervalle à utiliser.
async function resolveRefreshMs() {
  try {
    const res = await fetch("/api/config", { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const cfg = await res.json();
    readOnly = !!cfg.readonly;
    if (cfg.refresh_ms > 0) return cfg.refresh_ms;
  } catch (_) {
    // ignoré : on retombe sur la valeur par défaut
  }
  return DEFAULT_REFRESH_MS;
}

// showVersion récupère la version du binaire (injectée au build, exposée par
// /api/version) et l'affiche sous le titre. En cas d'échec, le libellé reste
// masqué.
async function showVersion() {
  const el = document.getElementById("version");
  if (!el) return;
  try {
    const res = await fetch("/api/version", { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const { version } = await res.json();
    if (version) {
      el.textContent = version;
      el.hidden = false;
    }
  } catch (_) {
    // version indisponible : on laisse le libellé masqué
  }
}

(async () => {
  showVersion(); // non bloquant : en parallèle de la résolution de l'intervalle
  setupProcToggle();
  setupDiskSelect();
  const intervalMs = await resolveRefreshMs();
  const footer = document.getElementById("refresh-label");
  if (footer) footer.textContent = `${intervalMs / 1000} s`;
  connect(intervalMs);
})();
