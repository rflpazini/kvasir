// Kvasir frontend — HTTP/URL layer.
// Pure functions that build URLs, fetch JSON, and probe /healthz. No DOM
// touching, no state — callers in app.js own the AbortController + the
// request lifecycle so cancellation stays inspectable from one place.

const BASE_LIMIT = "50";

/** Build a /api/search URL with optional quality filter. */
export function searchURL(query, qualities) {
    const params = new URLSearchParams({ q: query, limit: BASE_LIMIT });
    appendQualities(params, qualities);
    return `/api/search?${params.toString()}`;
}

/** Build a /api/recent URL with optional quality filter. */
export function recentURL(qualities) {
    const params = new URLSearchParams({ limit: BASE_LIMIT });
    appendQualities(params, qualities);
    return `/api/recent?${params.toString()}`;
}

function appendQualities(params, qualities) {
    if (qualities && qualities.size > 0) {
        params.set("quality", Array.from(qualities).join(","));
    }
}

/**
 * Fetch JSON, honoring an AbortSignal. Throws on non-2xx; returns parsed
 * JSON otherwise. AbortError bubbles up so the caller can distinguish
 * cancel from real failure (we do that in app.js — abort = no-op).
 */
export async function fetchJSON(url, signal) {
    const resp = await fetch(url, {
        signal,
        headers: { Accept: "application/json" },
    });
    if (!resp.ok) {
        throw new Error(`HTTP ${resp.status}`);
    }
    return resp.json();
}

/**
 * Pull the per-source health snapshot from /healthz. Returns an array of
 * { name, status, last_success_at, consecutive_failures, degraded }.
 * Soft-fails to [] so the UI still renders without chips on partial outages.
 */
export async function loadAdapters() {
    try {
        const data = await fetchJSON("/healthz");
        return data.adapters || [];
    } catch (err) {
        console.warn("kvasir: failed to load adapters", err);
        return [];
    }
}
