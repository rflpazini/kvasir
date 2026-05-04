// Kvasir — frontend logic.
// Single-user, single-page. No build step, no framework, no external deps.

(() => {
    "use strict";

    // ---- DOM refs ----------------------------------------------------------

    const form          = document.getElementById("search-form");
    const input         = document.getElementById("q");
    const results       = document.getElementById("results");
    const stats         = document.getElementById("stats");
    const statCount     = document.getElementById("stat-count");
    const statDuration  = document.getElementById("stat-duration");
    const statSources   = document.getElementById("stat-sources");
    const statsCache    = document.getElementById("stats-cache");
    const chipsHost     = document.getElementById("source-chips");
    const qualityHost   = document.getElementById("quality-chips");
    const initialNotice = document.getElementById("initial-notice");
    const tabSearch     = document.getElementById("tab-search");
    const tabRecent     = document.getElementById("tab-recent");
    const heroSearch    = document.getElementById("hero-search");
    const heroRecent    = document.getElementById("hero-recent");
    const recentRefresh = document.getElementById("recent-refresh");

    const tplSkeleton = document.getElementById("tpl-skeleton");
    const tplCard     = document.getElementById("tpl-card");
    const tplEmpty    = document.getElementById("tpl-empty");
    const tplError    = document.getElementById("tpl-error");

    // ---- state -------------------------------------------------------------

    /** @type {Set<string>} sources currently included in the view. */
    let activeSources = new Set();

    /** @type {Set<string>} quality buckets currently active (server-side filter). Empty = no filter. */
    let activeQualities = new Set();

    /** @type {object|null} latest /api/search payload, kept for client-side filter re-renders. */
    let lastResponse = null;

    /** @type {string|null} latest query, used by retry. */
    let lastQuery = null;

    /** @type {"search"|"recent"} active mode; toggled by the tab buttons. */
    let mode = "search";

    /** @type {AbortController|null} cancels an in-flight search when a newer one fires. */
    let inflight = null;

    // ---- helpers -----------------------------------------------------------

    /** Format size in bytes into a human-readable string, or em-dash on 0/missing. */
    function formatSize(bytes) {
        if (!bytes || bytes <= 0) return "—";
        const units = ["B", "KB", "MB", "GB", "TB"];
        let i = 0;
        let v = bytes;
        while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
        return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
    }

    function formatNumber(n) {
        if (!n || n <= 0) return "—";
        return new Intl.NumberFormat("pt-BR").format(n);
    }

    function el(tag, className, text) {
        const node = document.createElement(tag);
        if (className) node.className = className;
        if (text != null) node.textContent = text;
        return node;
    }

    function clearChildren(node) {
        while (node.firstChild) node.removeChild(node.firstChild);
    }

    function fromTemplate(tpl) {
        return tpl.content.firstElementChild.cloneNode(true);
    }

    // ---- bootstrap source chips -------------------------------------------

    /** Pull the list of registered adapters from /healthz and seed chips. */
    async function loadSources() {
        try {
            const resp = await fetch("/healthz", { headers: { Accept: "application/json" } });
            if (!resp.ok) throw new Error(`status ${resp.status}`);
            const data = await resp.json();
            const names = Object.keys(data.adapters || {}).sort();
            renderChips(names);
            names.forEach((n) => activeSources.add(n));
        } catch (err) {
            console.warn("kvasir: failed to load sources", err);
            // Soft-fail. Chips simply stay empty; backend will still return data.
        }
    }

    function renderChips(names) {
        clearChildren(chipsHost);
        if (names.length === 0) {
            chipsHost.appendChild(el("span", "filters__label", "(nenhuma registrada)"));
            return;
        }
        names.forEach((name) => {
            const chip = el("button", "chip", name);
            chip.type = "button";
            chip.dataset.source = name;
            chip.setAttribute("aria-pressed", "true");
            chip.addEventListener("click", () => toggleSource(name, chip));
            chipsHost.appendChild(chip);
        });
    }

    function bindQualityChips() {
        qualityHost.querySelectorAll(".chip[data-quality]").forEach((chip) => {
            chip.addEventListener("click", () => {
                const q = chip.dataset.quality;
                if (activeQualities.has(q)) {
                    activeQualities.delete(q);
                    chip.setAttribute("aria-pressed", "false");
                } else {
                    activeQualities.add(q);
                    chip.setAttribute("aria-pressed", "true");
                }
                refireCurrent();
            });
        });
    }

    function refireCurrent() {
        if (mode === "recent") {
            runRecent();
        } else if (lastQuery) {
            runSearch(lastQuery);
        }
    }

    function bindTabs() {
        tabSearch.addEventListener("click", () => activate("search"));
        tabRecent.addEventListener("click", () => activate("recent"));
        if (recentRefresh) {
            recentRefresh.addEventListener("click", () => runRecent());
        }
    }

    function activate(target) {
        if (target === mode) return;
        // Cancel any in-flight fetch so a slow search response cannot
        // race-render after we have already switched to Lançamentos.
        if (inflight) inflight.abort();
        mode = target;
        const isRecent = target === "recent";
        tabSearch.setAttribute("aria-selected", String(!isRecent));
        tabRecent.setAttribute("aria-selected", String(isRecent));
        heroSearch.hidden = isRecent;
        heroRecent.hidden = !isRecent;
        if (isRecent) {
            runRecent();
        }
    }

    function toggleSource(name, chip) {
        const isActive = activeSources.has(name);
        if (isActive && activeSources.size === 1) {
            // Don't allow zero-source state, would always render empty.
            return;
        }
        if (isActive) activeSources.delete(name);
        else activeSources.add(name);
        chip.setAttribute("aria-pressed", String(!isActive));
        if (lastResponse) renderResults(lastResponse);
    }

    // ---- search lifecycle --------------------------------------------------

    async function runSearch(query) {
        if (!query) return;
        lastQuery = query;
        await runQuery(buildSearchUrl(query));
    }

    async function runRecent() {
        await runQuery(buildRecentUrl());
    }

    function buildSearchUrl(query) {
        const params = new URLSearchParams({ q: query, limit: "50" });
        if (activeQualities.size > 0) {
            params.set("quality", Array.from(activeQualities).join(","));
        }
        return `/api/search?${params.toString()}`;
    }

    function buildRecentUrl() {
        const params = new URLSearchParams({ limit: "50" });
        if (activeQualities.size > 0) {
            params.set("quality", Array.from(activeQualities).join(","));
        }
        return `/api/recent?${params.toString()}`;
    }

    async function runQuery(url) {
        // cancel any prior request
        if (inflight) inflight.abort();
        inflight = new AbortController();

        showSkeleton();
        results.setAttribute("aria-busy", "true");

        try {
            const resp = await fetch(url, {
                signal: inflight.signal,
                headers: { Accept: "application/json" },
            });
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            lastResponse = data;
            renderResults(data);
        } catch (err) {
            if (err.name === "AbortError") return;
            console.error("kvasir: search failed", err);
            renderError(err.message || String(err));
        } finally {
            results.setAttribute("aria-busy", "false");
            inflight = null;
        }
    }

    // ---- rendering ---------------------------------------------------------

    function showSkeleton() {
        clearChildren(results);
        results.appendChild(fromTemplate(tplSkeleton));
    }

    function renderResults(data) {
        renderStats(data);
        clearChildren(results);

        const filtered = (data.results || []).filter((r) => activeSources.has(r.source));

        if (filtered.length === 0) {
            const empty = fromTemplate(tplEmpty);
            empty.querySelector("strong").textContent = `«${data.query || lastQuery || ""}»`;
            results.appendChild(empty);
            return;
        }

        const frag = document.createDocumentFragment();
        filtered.forEach((r, idx) => {
            const card = buildCard(r, idx);
            frag.appendChild(card);
        });
        results.appendChild(frag);
    }

    function buildCard(result, idx) {
        const card = fromTemplate(tplCard);
        card.style.animationDelay = `${Math.min(idx * 30, 300)}ms`;

        card.querySelector(".card__title").textContent = result.title || "(sem título)";
        card.querySelector(".card__source").textContent = result.source || "—";

        // quality badge — hidden when bucket is "Other" (noise) or missing.
        const qualityEl = card.querySelector(".card__quality");
        if (result.quality && result.quality !== "Other") {
            qualityEl.textContent = result.quality;
            qualityEl.dataset.quality = result.quality;
            qualityEl.hidden = false;
        }

        // metadata pills
        const meta = card.querySelector(".card__meta");
        meta.appendChild(buildPill("tamanho", formatSize(result.size_bytes)));
        meta.appendChild(buildPill("seeders", formatNumber(result.seeders)));
        meta.appendChild(buildPill("leechers", formatNumber(result.leechers)));
        if (result.category) {
            meta.appendChild(buildPill("categoria", result.category));
        }

        // open page action
        const openBtn = card.querySelector('[data-action="open"]');
        if (result.detail_url) {
            openBtn.href = result.detail_url;
        } else {
            openBtn.removeAttribute("href");
            openBtn.classList.add("btn");
            openBtn.setAttribute("aria-disabled", "true");
            openBtn.style.pointerEvents = "none";
            openBtn.style.opacity = "0.4";
        }

        // magnet copy
        const magnetBtn = card.querySelector('[data-action="copy-magnet"]');
        if (result.magnet) {
            magnetBtn.disabled = false;
            magnetBtn.dataset.magnet = result.magnet;
            magnetBtn.addEventListener("click", () => copyMagnet(magnetBtn));
        }

        return card;
    }

    function buildPill(label, value) {
        const isEmpty = value === "—";
        const pill = el("span", "pill" + (isEmpty ? " pill--empty" : ""));
        pill.appendChild(el("span", "pill__label", label));
        pill.appendChild(el("span", "pill__value", value));
        return pill;
    }

    function renderStats(data) {
        const total = (data.results || []).length;
        statCount.textContent = String(total);
        statDuration.textContent = String(data.duration_ms ?? 0);

        const sourceParts = Object.entries(data.source_stats || {}).map(([name, stat]) => {
            const status = stat.status || "—";
            return status === "ok" ? `${name} ${stat.count}` : `${name} (${status})`;
        });
        statSources.textContent = sourceParts.length === 0 ? "—" : sourceParts.join("  ·  ");

        if (data.cached) {
            statsCache.hidden = false;
        } else {
            statsCache.hidden = true;
        }

        stats.hidden = false;
        if (initialNotice && initialNotice.parentNode) {
            initialNotice.remove();
        }
    }

    function renderError(msg) {
        clearChildren(results);
        const node = fromTemplate(tplError);
        node.querySelector("code").textContent = msg;
        node.querySelector('[data-action="retry"]').addEventListener("click", () => {
            if (lastQuery) runSearch(lastQuery);
        });
        results.appendChild(node);
    }

    // ---- magnet copy -------------------------------------------------------

    async function copyMagnet(btn) {
        const magnet = btn.dataset.magnet;
        if (!magnet) return;
        try {
            await navigator.clipboard.writeText(magnet);
            btn.dataset.state = "copied";
            const label = btn.querySelector("[data-copy-label]");
            const original = label.textContent;
            label.textContent = "Copiado";
            setTimeout(() => {
                btn.removeAttribute("data-state");
                label.textContent = original;
            }, 1400);
        } catch (err) {
            console.error("kvasir: clipboard write failed", err);
        }
    }

    // ---- keyboard shortcuts ------------------------------------------------

    function bindShortcuts() {
        document.addEventListener("keydown", (event) => {
            // Cmd/Ctrl + K focuses the search input
            if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
                event.preventDefault();
                input.focus();
                input.select();
                return;
            }
            // Esc clears the input when it's focused
            if (event.key === "Escape" && document.activeElement === input) {
                input.value = "";
            }
        });
    }

    // ---- init --------------------------------------------------------------

    form.addEventListener("submit", (event) => {
        event.preventDefault();
        const q = input.value.trim();
        if (!q) return;
        if (mode !== "search") activate("search");
        runSearch(q);
    });

    bindShortcuts();
    bindQualityChips();
    bindTabs();
    loadSources();
})();
