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

async function refresh() {
    try {
        const res = await fetch("/api/system", { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = await res.json();

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

        const host = data.host;
        document.getElementById("host-list").innerHTML = `
          <li><span class="key">🏠 Nom</span><span class="val">${host.hostname || "—"}</span></li>
          <li><span class="key">🖥️ Système</span><span class="val">${host.platform || host.os || "—"}</span></li>
          <li><span class="key">🏗️ Architecture</span><span class="val">${host.kernel_arch || "—"}</span></li>
          <li><span class="key">⏱️ Uptime</span><span class="val">${formatUptime(host.uptime_seconds)}</span></li>
          <li><span class="key">🐹 Go</span><span class="val">${host.go_version || "—"}</span></li>
        `;

        const time = new Date(data.timestamp).toLocaleTimeString("fr-FR");
        setStatus(true, `Mis à jour à ${time}`);
    } catch (err) {
        setStatus(false, `Hors ligne : ${err.message}`);
    }
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
    await refresh();
    setInterval(refresh, intervalMs);
})();
