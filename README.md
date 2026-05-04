# Kvasir

Private meta-search aggregator for PT-BR torrent indexers, fanning out a single query across multiple sources and collapsing the responses into one normalized JSON contract.

> **For homelab use only.** This stack is single-user, behind a `lan-only` Traefik middleware, and serves no traffic outside the local network. There is no public deployment, no analytics, no SEO, and no content republishing. The repo is open-source as a Docker / Go reference implementation.

## Why "Kvasir"

In Norse mythology, Kvasir was created from the saliva of the Æsir and Vanir gods after the truce that ended their war. He was so wise that nobody could ask a question he did not know how to answer; his blood was later turned into the mead of poetry, the source of all wisdom. The name fits an aggregator whose job is to consult every available source and return a single answer.

## Architecture at a glance

```
                       LAN / Tailscale only
                                |
                       Traefik (lan-only mw)
                                |
                  +-------------+-------------+
                  |                           |
              Frontend                  Echo v4 API
            (HTML + HTMX)                    |
                            +--------+-------+--------+
                            |                |        |
                       Adapter A         Adapter B    ...
                       (colly)           (colly)
                                                       |
                            +-------------+------------+
                            |                          |
                      FlareSolverr                Redis cache
                      (CF bypass)                 (TTL 15 min)
```

Mono-binary Go service. Each adapter implements a small interface (`Search(ctx, query)`); the aggregator fans them out in parallel via `errgroup` with per-adapter timeout and best-effort semantics (one slow or failing source never derails the rest). Results are merged, deduplicated, cached, and served as JSON to a vanilla-JS frontend.

## Stack

- **Go 1.26** with `log/slog` for structured logging
- **Echo v4** as the HTTP router
- **colly v2** + `goquery` for HTML scraping
- **chromedp** is intentionally **not** used; FlareSolverr is the only browser-engine dependency, and only when an adapter detects a Cloudflare challenge
- **FlareSolverr** for Cloudflare bypass, with session reuse so cold solves only happen once per browser context (drops cold p95 from ~13s to ~2s)
- **Redis 7** for query caching (full result set stored, `limit` applied in-memory after the lookup) and for an atomic Lua-backed token bucket per adapter
- **Prometheus** for metrics (latency histograms, cache hit/miss counters, per-adapter consecutive-failure gauge)
- **Traefik** as the reverse proxy, in production behind a `lan-only` IP allow-list and a `secured-defaults` header chain
- Frontend is plain HTML/CSS/JS — no build step, no framework, no external CDN trackers

## Repository layout

```
kvasir/
├── cmd/server/             entrypoint + Docker HEALTHCHECK subcommand
├── internal/
│   ├── adapter/            site adapters + parser tests + golden fixtures
│   ├── aggregator/         parallel fan-out + dedupe
│   ├── cache/              Redis client + Lua token bucket
│   ├── flaresolverr/       HTTP client with lazy session reuse
│   ├── http/               Echo handlers, middleware, frontend serving
│   ├── model/              Result, SearchResponse
│   └── observability/      slog logger + Prometheus metrics
├── web/static/             HTML / CSS / vanilla JS
├── scripts/
│   ├── capture-fixture.sh  fetch a real search response into testdata/
│   └── perf-check.sh       cold + warm p95 measurement loop
├── testdata/queries.txt    20 fixed queries for the SLO suite
└── deploy/
    ├── Dockerfile          multi-stage, distroless final (~18 MB)
    ├── docker-compose.yml          production stack on the homelab
    └── docker-compose.dev.yml      local Redis + FlareSolverr only
```

## Adding a new adapter

The contribution flow is **TDD-first** with golden HTML fixtures. Skipping the fixture step is the most common cause of silent regressions when an upstream site tweaks its markup.

1. `scripts/capture-fixture.sh <site> "<query>"` saves the live response into `internal/adapter/testdata/<site>/search_<slug>.html`.
2. Write `internal/adapter/<site>_test.go` with a parser test that loads the fixture and asserts on the normalized result slice. The test must fail at this point because the parser does not exist.
3. Implement `internal/adapter/<site>.go`, exposing a pure `Parse<Site>([]byte) ([]model.Result, error)` plus the `adapter.Adapter` interface methods, until the test goes green.
4. Register the adapter in `cmd/server/main.go`.
5. Run `go test ./...` and confirm coverage stays above 80% on `./internal/...`.

A re-capture of the fixture is required whenever the parser starts failing in production after a site HTML change. The metric `kvasir_adapter_consecutive_failures` is the signal that triggers this.

## Local development

```bash
# Start Redis and FlareSolverr only; the Go binary runs on the host.
docker compose -f deploy/docker-compose.dev.yml up -d

REDIS_ADDR=localhost:6379 \
FLARESOLVERR_URL=http://localhost:8191/v1 \
go run ./cmd/server
```

The server listens on `:8080` by default. The frontend is served from `web/static/`; `/api/search?q=...` returns JSON; `/healthz` and `/metrics` are exposed for ops.

## Tests and coverage

```bash
go test ./...
go test -coverprofile=cover.out -coverpkg=./internal/... ./...
go tool cover -func=cover.out | tail -1
```

The suite is hermetic: `miniredis` for the cache layer, `httptest` for HTTP/FlareSolverr, custom `http.RoundTripper` to exercise adapter Search/HealthCheck plumbing without hitting the real sites. The 80% gate is enforced on everything under `./internal/...` except `internal/observability/metrics.go` (collector registration is boilerplate).

## SLO validation

```bash
HOST=http://localhost:8080 \
REDIS_CONTAINER=kvasir-dev-redis \
./scripts/perf-check.sh
```

The script runs the 20 fixed queries cold (with an explicit `FLUSHDB` first), then warm, and prints p50/p95 for each pass. Targets:

| Metric | Phase 1 | Phase 2 |
|--------|---------|---------|
| Cold p95 | < 5 s | < 5 s |
| Warm p95 | < 2 s | < 3 s |

Cold latency is dominated by FlareSolverr the first time a CF-protected source is queried in a fresh session; subsequent cold queries hit the warmed browser context and land around 2 s.

## Production deployment

The production stack lives on a Proxmox LXC running Docker. Bring it up with:

```bash
cd /appdata/kvasir
docker compose -f deploy/docker-compose.yml up -d --build
```

The kvasir container is dual-homed: it joins the existing `edge` network so Traefik can reach it via Docker-provider labels, and a private `kvasir-net` so Redis and FlareSolverr stay off the public network. The route is gated by `lan-only@file` (IP allow-list) and `secured-defaults@file` (security headers chain). LAN clients reach it via a Pi-hole DNS override that resolves the public hostname to the Traefik IP, bypassing the upstream Cloudflare path entirely.

## License

Personal homelab project, released for reference purposes. No warranty, no support commitment.
