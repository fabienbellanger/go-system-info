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
    arc.style.color = color; // alimente le drop-shadow
    arc.style.stroke = color;
    arc.style.strokeDashoffset = CIRCUMFERENCE * (1 - Math.min(pct, 100) / 100);
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

// formatRate met en forme un débit (octets/s) en unités décimales lisibles,
// cohérentes avec l'affichage des Go (base 1000) du reste de l'interface.
function formatRate(bytesPerSec) {
    const units = ["o/s", "Ko/s", "Mo/s", "Go/s"];
    let v = Math.max(bytesPerSec, 0);
    let i = 0;
    while (v >= 1000 && i < units.length - 1) {
        v /= 1000;
        i++;
    }
    // Une décimale pour les unités >= Ko/s tant que la valeur reste petite.
    const digits = i > 0 && v < 100 ? 1 : 0;
    return `${v.toFixed(digits)} ${units[i]}`;
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

function setStatus(ok, text) {
    document.getElementById("status-dot").classList.toggle("error", !ok);
    // Le texte n'est plus affiché : conservé en infobulle au survol du badge.
    document.getElementById("status").title = text;
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

    const host = data.host;
    document.getElementById("host-list").innerHTML = `
      <li><span class="key">🏠 Nom</span><span class="val">${host.hostname || "—"}</span></li>
      <li><span class="key">🖥️ Système</span><span class="val">${host.platform || host.os || "—"}</span></li>
      <li><span class="key">🏗️ Architecture</span><span class="val">${host.kernel_arch || "—"}</span></li>
      <li><span class="key">📈 Charge</span><span class="val">${formatLoad(data.load)}</span></li>
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
    setStatus(true, `Mis à jour à ${time}`);
}

// connect ouvre le flux SSE et met à jour l'interface à chaque événement.
// EventSource gère la reconnexion automatiquement en cas de coupure.
function connect() {
    const source = new EventSource("/api/stream");

    source.onmessage = (event) => {
        try {
            applyState(JSON.parse(event.data));
        } catch (err) {
            setStatus(false, `Données invalides : ${err.message}`);
        }
    };

    source.onerror = () => {
        // La connexion est rompue : EventSource tentera de se reconnecter seul.
        setStatus(false, "Hors ligne : reconnexion…");
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

(async () => {
    const intervalMs = await resolveRefreshMs();
    const footer = document.getElementById("refresh-label");
    if (footer) footer.textContent = `${intervalMs / 1000} s`;
    connect();
})();
