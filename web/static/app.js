// Kvasir frontend — top-level state + event wiring.
// Loaded as <script type="module">. Pure logic + UI plumbing lives in
// api.js (URL building / fetch) and render.js (DOM helpers / templates);
// this module owns the mutable session state and dispatches between them.
//
// The ?v=src1 querystring on each import is load-bearing despite the
// `Cache-Control: no-cache` middleware. Cloudflare overrides origin
// cache headers with a zone-level rule (max-age=14400 surfaces in the
// CF response), so without versioned URLs CF holds an old api.js or
// render.js for hours after a deploy. Bump v on every change to
// either module — single source of truth: search-and-replace `v=src1`
// in index.html (stylesheet + script tag) AND here.
import { fetchJSON, fetchMagnet, loadAdapters, recentURL, searchURL } from "/api.js?v=src1";
import {
    renderError,
    renderResults,
    renderSourceChips,
    renderStats,
    showSkeleton,
} from "/render.js?v=src1";

// ---- DOM refs --------------------------------------------------------------

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

const statsRefs = { stats, statCount, statDuration, statSources, statsCache, initialNotice };

// ---- state -----------------------------------------------------------------

const activeSources   = new Set();
const activeQualities = new Set();
let lastResponse = null;
let lastQuery = null;
let mode = "search";
let inflight = null;

// ---- search lifecycle ------------------------------------------------------

function runSearch(query) {
    if (!query) return Promise.resolve();
    lastQuery = query;
    return runQuery(searchURL(query, activeQualities));
}

function runRecent() {
    return runQuery(recentURL(activeQualities));
}

async function runQuery(url) {
    if (inflight) inflight.abort();
    inflight = new AbortController();

    showSkeleton(results, tplSkeleton);
    results.setAttribute("aria-busy", "true");

    try {
        const data = await fetchJSON(url, inflight.signal);
        lastResponse = data;
        paint(data);
    } catch (err) {
        if (err.name === "AbortError") return;
        console.error("kvasir: query failed", err);
        renderError(results, tplError, err.message || String(err), retry);
    } finally {
        results.setAttribute("aria-busy", "false");
        inflight = null;
    }
}

function paint(data) {
    renderStats(statsRefs, data);
    renderResults(results, data, {
        activeSources,
        tplCard,
        tplEmpty,
        onCopyMagnet: copyMagnet,
        fallbackQuery: lastQuery,
    });
}

// retry intentionally re-reads module-level `mode` and `lastQuery` at
// click time. Don't snapshot them as args — the user can tab + retry.
function retry() {
    if (mode === "recent") runRecent();
    else if (lastQuery) runSearch(lastQuery);
}

// ---- chips + tabs ----------------------------------------------------------

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
    if (mode === "recent") runRecent();
    else if (lastQuery) runSearch(lastQuery);
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
    // race-render after we have already switched tabs.
    if (inflight) inflight.abort();
    mode = target;
    const isRecent = target === "recent";
    tabSearch.setAttribute("aria-selected", String(!isRecent));
    tabRecent.setAttribute("aria-selected", String(isRecent));
    heroSearch.hidden = isRecent;
    heroRecent.hidden = !isRecent;
    if (isRecent) runRecent();
}

function toggleSource(name, chip) {
    const isActive = activeSources.has(name);
    if (isActive && activeSources.size === 1) {
        // Don't allow zero-source state — would always render empty.
        return;
    }
    if (isActive) activeSources.delete(name);
    else activeSources.add(name);
    chip.setAttribute("aria-pressed", String(!isActive));
    if (lastResponse) paint(lastResponse);
}

async function bootstrapSourceChips() {
    const adapters = await loadAdapters();
    adapters.forEach((a) => activeSources.add(a.name));
    renderSourceChips(chipsHost, adapters, toggleSource);
}

// ---- magnet copy -----------------------------------------------------------

async function copyMagnet(btn) {
    const source = btn.dataset.source;
    const detail = btn.dataset.detail;
    if (!source || !detail) return;

    const label = btn.querySelector("[data-copy-label]");
    const original = label.textContent;
    btn.disabled = true;
    label.textContent = "Buscando…";

    try {
        const magnet = await fetchMagnet(source, detail);
        // Card may have been re-rendered (search/tab/filter) while we
        // awaited; the new card has its own button. Bail before mutating
        // a detached node.
        if (!btn.isConnected) return;
        if (!magnet) {
            // Source does not expose magnets (ErrMagnetUnsupported on the
            // backend). Hide the button entirely so the user does not click
            // again expecting a different result.
            btn.style.display = "none";
            return;
        }
        await navigator.clipboard.writeText(magnet);
        btn.dataset.state = "copied";
        label.textContent = "Copiado";
        setTimeout(() => {
            if (!btn.isConnected) return;
            btn.removeAttribute("data-state");
            label.textContent = original;
            btn.disabled = false;
        }, 1400);
    } catch (err) {
        console.error("kvasir: magnet copy failed", err);
        if (!btn.isConnected) return;
        label.textContent = "Falhou";
        setTimeout(() => {
            if (!btn.isConnected) return;
            label.textContent = original;
            btn.disabled = false;
        }, 1600);
    }
}

// ---- keyboard --------------------------------------------------------------

function bindShortcuts() {
    document.addEventListener("keydown", (event) => {
        if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
            event.preventDefault();
            input.focus();
            input.select();
            return;
        }
        if (event.key === "Escape" && document.activeElement === input) {
            input.value = "";
        }
    });
}

// ---- init ------------------------------------------------------------------

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
bootstrapSourceChips();
