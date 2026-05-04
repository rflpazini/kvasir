#!/usr/bin/env bash
# capture-fixture.sh — capture real HTML response from a torrent site for
# golden-fixture testing.
#
# Stores output under internal/adapter/testdata/<adapter>/search_<query_slug>.html
# Always run before writing the parser. Re-run when the parser starts failing
# in production after a site HTML change.
#
# Usage:
#   ./scripts/capture-fixture.sh <adapter_name> <query>
#   ./scripts/capture-fixture.sh boitorrent "matrix 1080p"

set -euo pipefail

if [[ $# -lt 2 ]]; then
    cat >&2 <<EOF
usage: $0 <adapter_name> <query>

Optional env:
  SEARCH_URL_TEMPLATE  format string with %s placeholder for query
                       default: requires per-adapter override below
  USER_AGENT           default: realistic Chrome desktop
EOF
    exit 64
fi

ADAPTER="$1"
QUERY="$2"

SLUG=$(printf %s "$QUERY" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '_' | sed 's/^_//; s/_$//')
OUT_DIR="internal/adapter/testdata/${ADAPTER}"
OUT_FILE="${OUT_DIR}/search_${SLUG}.html"

UA="${USER_AGENT:-Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36}"

# Per-adapter URL templates. Add new adapters here.
declare -A URL_TEMPLATES=(
    [boitorrent]="https://boitorrent.com/?s=%s"
    [comando]="https://comando.la/?s=%s"
)

template="${SEARCH_URL_TEMPLATE:-${URL_TEMPLATES[$ADAPTER]:-}}"
if [[ -z "$template" ]]; then
    echo "fatal: no URL template for adapter '$ADAPTER'. Add it to URL_TEMPLATES or pass SEARCH_URL_TEMPLATE." >&2
    exit 1
fi

# urlencode the query before substituting into the template.
encoded=$(printf %s "$QUERY" | jq -sRr @uri)
url=$(printf "$template" "$encoded")

mkdir -p "$OUT_DIR"

echo "adapter:  $ADAPTER"
echo "query:    $QUERY"
echo "url:      $url"
echo "output:   $OUT_FILE"

curl -sS \
    --fail \
    --compressed \
    -H "User-Agent: $UA" \
    -H "Accept: text/html,application/xhtml+xml,application/xml;q=0.9" \
    -H "Accept-Language: pt-BR,pt;q=0.9,en;q=0.8" \
    "$url" \
    -o "$OUT_FILE"

bytes=$(wc -c < "$OUT_FILE" | tr -d ' ')
echo "captured ${bytes} bytes"

if [[ "$bytes" -lt 1024 ]]; then
    echo "warning: response is suspiciously small (<1KB). Check for CF challenge or block." >&2
fi
