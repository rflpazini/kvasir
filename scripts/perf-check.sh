#!/usr/bin/env bash
# perf-check.sh — SLO validation suite for kvasir.
#
# Cold p95 target  <5s   (Phase 1: 1 adapter)
# Warm p95 target  <2s   (Phase 1)
# Warm p95 target  <3s   (Phase 2: 4+ adapters paralelos)
#
# Usage:
#   HOST=https://kvasir.lan.rflpazini.sh ./scripts/perf-check.sh
#   HOST=http://localhost:8080 REDIS_CONTAINER=kvasir-dev-redis ./scripts/perf-check.sh

set -euo pipefail

HOST="${HOST:-http://localhost:8080}"
QUERIES_FILE="${QUERIES_FILE:-testdata/queries.txt}"
REDIS_CONTAINER="${REDIS_CONTAINER:-kvasir-redis}"

if [[ ! -f "$QUERIES_FILE" ]]; then
    echo "fatal: queries file not found at $QUERIES_FILE" >&2
    exit 1
fi

flush_cache() {
    if ! docker exec "$REDIS_CONTAINER" redis-cli FLUSHDB >/dev/null; then
        echo "fatal: failed to FLUSHDB on $REDIS_CONTAINER (set REDIS_CONTAINER env)" >&2
        exit 1
    fi
}

urlencode() {
    # POSIX-portable, jq presence required.
    printf %s "$1" | jq -sRr @uri
}

run_suite() {
    local label="$1"
    local out_file="/tmp/kvasir-perf-${label}.txt"
    : > "$out_file"

    echo "=== ${label} ==="
    while IFS= read -r q; do
        [[ -z "$q" ]] && continue
        local t
        t=$(curl -s -o /dev/null -w '%{time_total}' "${HOST}/api/search?q=$(urlencode "$q")")
        printf "%6.3fs  %s\n" "$t" "$q" | tee -a "$out_file"
    done < "$QUERIES_FILE"

    awk '{print $1}' "$out_file" | sort -n | awk '
        BEGIN { c = 0 }
        { v[c++] = $1 + 0 }
        END {
            if (c == 0) { print "no samples"; exit 1 }
            p50 = v[int(c * 0.50)]
            p95 = v[int(c * 0.95)]
            printf "p50: %.3fs  p95: %.3fs  (n=%d)\n", p50, p95, c
        }
    '
}

echo "host:           $HOST"
echo "queries:        $QUERIES_FILE"
echo "redis container: $REDIS_CONTAINER"
echo

flush_cache
run_suite "cold"

# Warm-up: 1ª passada popula o cache. 2ª passada é a medição warm.
echo
echo "warming cache..."
while IFS= read -r q; do
    [[ -z "$q" ]] && continue
    curl -s -o /dev/null "${HOST}/api/search?q=$(urlencode "$q")"
done < "$QUERIES_FILE"
echo

run_suite "warm"
