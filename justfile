bootstrap:
    uv sync && pnpm install && cargo fetch

fetch-models:
    uv run python -m repoctl fetch-models --manifest models/manifests

lint:
    uv run ruff check . && uv run basedpyright && pnpm lint && cargo clippy

test:
    uv run pytest -m "not slow" && pnpm test

gate ASSET LEVEL="all":
    uv run gaterunner run --asset {{ASSET}} --level {{LEVEL}} --report reports/gates/{{ASSET}}.json

journeys:
    uv run journeys run --set golden --seeds 3

budgets:
    uv run budgets check --report reports/nightly/latency.json

coverage:
    uv run python -m repoctl coverage && uv run python -m repoctl agents-md check && uv run python -m repoctl forbidden-refs

verify:
    uv run gaterunner verify-configs && just coverage

nightly-local:
    just gate all g0 && just journeys && just budgets && just coverage
