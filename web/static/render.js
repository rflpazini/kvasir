// Kvasir frontend — DOM rendering layer.
// Pure functions that take a DOM root + a payload and produce nodes.
// All app state lives in app.js; this module receives what it needs as
// arguments so a future test harness can render against jsdom without
// importing the whole app lifecycle.

// ---- formatters --------------------------------------------------------

export function formatSize(bytes) {
    if (!bytes || bytes <= 0) return "—";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let v = bytes;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function formatNumber(n) {
    if (!n || n <= 0) return "—";
    return new Intl.NumberFormat("pt-BR").format(n);
}

// ---- DOM helpers -------------------------------------------------------

export function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
}

export function clearChildren(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
}

export function fromTemplate(tpl) {
    return tpl.content.firstElementChild.cloneNode(true);
}

// ---- card ---------------------------------------------------------------

/**
 * Build a result card from the template. `tpl` is the cloneable <template>
 * element; `onCopyMagnet` is invoked when the magnet button is clicked.
 */
export function buildCard(result, idx, tpl, onCopyMagnet) {
    const card = fromTemplate(tpl);
    card.style.animationDelay = `${Math.min(idx * 30, 300)}ms`;

    card.querySelector(".card__title").textContent = result.title || "(sem título)";
    card.querySelector(".card__source").textContent = result.source || "—";

    // poster — when present, swap from the rune fallback to the lazy <img>.
    // onerror keeps the image hidden so a broken link keeps the runic glyph.
    const posterEl = card.querySelector(".card__poster");
    const posterImg = card.querySelector(".card__poster-img");
    if (result.poster_url) {
        posterImg.alt = result.title || "";
        const onLoad = () => { posterEl.dataset.state = "loaded"; };
        const onError = () => { posterEl.dataset.state = "empty"; };
        // {once:true} both detaches handlers after they fire and avoids
        // the "load fires twice" quirk seen on cached posters in Chrome,
        // where the synchronous complete-check below + the deferred event
        // would otherwise mutate state on a detached card.
        posterImg.addEventListener("load", onLoad, { once: true });
        posterImg.addEventListener("error", onError, { once: true });
        posterImg.src = result.poster_url;
        // Browsers can complete cached loads before the listeners fire
        // (especially with lazy-loaded images that the IntersectionObserver
        // resolves synchronously). Re-check after assignment.
        if (posterImg.complete) {
            if (posterImg.naturalWidth > 0) onLoad();
            else onError();
        }
    } else {
        posterEl.dataset.state = "empty";
    }

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
        magnetBtn.addEventListener("click", () => onCopyMagnet(magnetBtn));
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

// ---- collections --------------------------------------------------------

export function showSkeleton(rootEl, tpl) {
    clearChildren(rootEl);
    rootEl.appendChild(fromTemplate(tpl));
}

/**
 * Render the search-response payload into `rootEl`.
 * `ctx` carries the templates, source filter, magnet callback, and the
 * fallback query for the empty state. Bag-of-args at the call site keeps
 * positional ordering off the cognitive critical path.
 */
export function renderResults(rootEl, data, ctx) {
    const { activeSources, tplCard, tplEmpty, onCopyMagnet, fallbackQuery } = ctx;
    clearChildren(rootEl);

    const filtered = (data.results || []).filter((r) => activeSources.has(r.source));

    if (filtered.length === 0) {
        const empty = fromTemplate(tplEmpty);
        empty.querySelector("strong").textContent = `«${data.query || fallbackQuery || ""}»`;
        rootEl.appendChild(empty);
        return;
    }

    const frag = document.createDocumentFragment();
    filtered.forEach((r, idx) => frag.appendChild(buildCard(r, idx, tplCard, onCopyMagnet)));
    rootEl.appendChild(frag);
}

/**
 * Update the stats strip from a search payload. `refs` carries the four
 * span elements + the cache indicator + the initial-notice span we hide
 * after the first response.
 */
export function renderStats(refs, data) {
    const total = (data.results || []).length;
    refs.statCount.textContent = String(total);
    refs.statDuration.textContent = String(data.duration_ms ?? 0);

    const sourceParts = Object.entries(data.source_stats || {}).map(([name, stat]) => {
        const status = stat.status || "—";
        return status === "ok" ? `${name} ${stat.count}` : `${name} (${status})`;
    });
    refs.statSources.textContent = sourceParts.length === 0 ? "—" : sourceParts.join("  ·  ");

    refs.statsCache.hidden = !data.cached;
    refs.stats.hidden = false;

    if (refs.initialNotice && refs.initialNotice.parentNode) {
        refs.initialNotice.remove();
    }
}

export function renderError(rootEl, tpl, msg, onRetry) {
    clearChildren(rootEl);
    const node = fromTemplate(tpl);
    node.querySelector("code").textContent = msg;
    node.querySelector('[data-action="retry"]').addEventListener("click", onRetry);
    rootEl.appendChild(node);
}

/** Render the source-filter chips. `onToggle(name, chip)` mutates the active set. */
export function renderSourceChips(host, names, onToggle) {
    clearChildren(host);
    if (names.length === 0) {
        host.appendChild(el("span", "filters__label", "(nenhuma registrada)"));
        return;
    }
    names.forEach((name) => {
        const chip = el("button", "chip", name);
        chip.type = "button";
        chip.dataset.source = name;
        chip.setAttribute("aria-pressed", "true");
        chip.addEventListener("click", () => onToggle(name, chip));
        host.appendChild(chip);
    });
}
