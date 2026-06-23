const DEFAULT_REFRESH_MS = 3000; // valeur de repli si /api/config est indisponible
const CIRCUMFERENCE = 2 * Math.PI * 60; // r = 60

// Couleur selon le seuil d'utilisation.
function colorFor(pct) {
    if (pct >= 90) return "var(--red)";
    if (pct >= 70) return "var(--orange)";
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

// applyState met à jour l'interface à partir d'un état poussé par le flux SSE
// ({ system, history }).
function applyState(state) {
    const data = state.system;

    updateGauge("cpu", data.cpu.used_percent, `${data.cpu.cores} cœurs`, data.cpu.model_name || "CPU");

    updateGauge(
        "mem",
        data.memory.used_percent,
        `${data.memory.used_gb.toFixed(1)} / ${data.memory.total_gb.toFixed(1)} Go`,
        `${data.memory.free_gb.toFixed(1)} Go libres`,
    );

    updateGauge(
        "disk",
        data.disk.used_percent,
        `${(data.disk.total_gb - data.disk.used_gb).toFixed(0)} / ${data.disk.total_gb.toFixed(0)} Go restant`,
        `Montage ${data.disk.path}<span class="note" title="L'espace purgeable (snapshots Time Machine locaux, caches récupérables automatiquement par macOS) n'est pas compté comme disponible ici, contrairement au Finder ou à CleanMyMac. La valeur reflète l'espace réellement libre au sens du système de fichiers.">ℹ️ Espace purgeable non inclus</span>`,
    );

    const net = data.net || {};
    document.getElementById("net-recv").textContent = formatRate(net.recv_bytes_per_sec || 0);
    document.getElementById("net-sent").textContent = formatRate(net.sent_bytes_per_sec || 0);
    document.getElementById("net-recv-total").textContent = formatBytes(net.recv_total_bytes || 0);
    document.getElementById("net-sent-total").textContent = formatBytes(net.sent_total_bytes || 0);

    const host = data.host;
    const cores = data.cpu.cores;
    // Charge : on affiche le triplet 1/5/15 min, suivi d'un repère « · N cœurs »
    // pour situer la valeur, et une infobulle explique la lecture.
    const loadText = data.load
        ? `${formatLoad(data.load)}<span class="ref">· ${cores} cœurs</span>`
        : "—";
    const loadTitle =
        `Charge système moyenne (load average) sur 1, 5 et 15 min : nombre moyen de processus ` +
        `actifs ou en attente du CPU. À comparer aux ${cores} cœurs — en dessous il reste de la ` +
        `marge, au-dessus le système est surchargé. Ce n'est pas un pourcentage CPU.`;
    document.getElementById("host-list").innerHTML = `
      <li><span class="key">🏠 Nom</span><span class="val">${host.hostname || "—"}</span></li>
      <li><span class="key">🖥️ Système</span><span class="val">${host.platform || host.os || "—"}</span></li>
      <li><span class="key">🏗️ Architecture</span><span class="val">${host.kernel_arch || "—"}</span></li>
      <li title="${loadTitle}"><span class="key">📈 Charge</span><span class="val">${loadText}</span></li>
      <li><span class="key">⏱️ Uptime</span><span class="val">${formatUptime(host.uptime_seconds)}</span></li>
      <li><span class="key">🐹 Go</span><span class="val">${host.go_version || "—"}</span></li>
    `;

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

// Récupère l'intervalle de rafraîchissement défini côté serveur (flag -r).
async function resolveRefreshMs() {
    try {
        const res = await fetch("/api/config", { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const cfg = await res.json();
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
    const intervalMs = await resolveRefreshMs();
    const footer = document.getElementById("refresh-label");
    if (footer) footer.textContent = `${intervalMs / 1000} s`;
    connect(intervalMs);
})();
