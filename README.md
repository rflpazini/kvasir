# Kvasir

Meta-search privado de torrents PT-BR, agregando múltiplas fontes num contrato único.

## Origem do nome

Na mitologia nórdica, Kvasir foi criado do cuspe dos deuses Æsir e Vanir após a guerra entre os panteões. Era tão sábio que ninguém conseguia fazer uma pergunta que ele não soubesse responder. Seu sangue virou o hidromel da poesia, fonte de toda sabedoria.

Aqui, Kvasir agrega o conhecimento espalhado em múltiplos sites pra responder qualquer query.

## Privacidade

Esta stack é **privada**. Acesso restrito a Tailscale/LAN via Traefik com middleware `lan-only`. Sem exposição pública, sem analytics, sem SEO, sem republicação de conteúdo. Single-user, low rate, scrape consciente.

Se cair em rede pública, o middleware do Traefik bloqueia. Validação obrigatória após deploy: `curl` externo retornando 403/timeout.

## Stack

- Go 1.26
- Echo v4 (HTTP router)
- colly v2 (scraping HTML)
- Redis 7 (cache)
- FlareSolverr (Cloudflare bypass)
- Prometheus (métricas)
- HTMX + plain HTML (frontend)
- Traefik (reverse proxy, externo)

## Estrutura

```
kvasir/
├── cmd/server/             # entrypoint + healthcheck subcommand
├── internal/
│   ├── adapter/            # interface + impls + golden fixtures
│   ├── aggregator/         # paralelismo errgroup + dedupe
│   ├── cache/              # Redis client + Lua scripts
│   ├── flaresolverr/       # client HTTP
│   ├── http/               # Echo routes + handlers + middleware
│   ├── model/              # Result, SearchResponse
│   └── observability/      # metrics + slog logger
├── web/static/             # frontend HTML/HTMX
├── scripts/                # perf-check, capture-fixture
├── testdata/               # queries de validação SLO
└── deploy/                 # docker-compose, Dockerfile
```

## Adicionar novo adapter

**Sequência obrigatória (TDD-first):**

1. `scripts/capture-fixture.sh <site> <query>` captura HTML real em `internal/adapter/testdata/<site>/search_<query>.html`
2. Escreva `<site>_test.go` com parser test que carrega a fixture e espera resultados normalizados. Test FALHA (parser ainda não existe).
3. Implemente `<site>.go` até o test passar.
4. Refactor com test verde.

Não pule. Mesmo que o parser pareça trivial.

## Desenvolvimento local

```bash
docker compose -f deploy/docker-compose.dev.yml up -d redis flaresolverr
go run ./cmd/server
```

## Testes

```bash
go test -coverprofile=cover.out -coverpkg=./internal/... ./...
go tool cover -func=cover.out | tail -1
```

Gate: ≥80% em todo `internal/` exceto `internal/observability/metrics.go`.

## Validação SLO

```bash
HOST=https://kvasir.lan.rflpazini.sh ./scripts/perf-check.sh
```

Critérios: cold p95 <5s, warm p95 <2s (Fase 1), warm p95 <3s com paralelismo (Fase 2).

## Documentação completa

Plano arquitetural: `~/Library/Mobile Documents/iCloud~md~obsidian/Documents/rflpazini/notes/torrent-search-homelab-plan-2026-05-03.md`
