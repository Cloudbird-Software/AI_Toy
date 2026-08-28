# ai-toy 根任务入口（spec §3.9）。等效改写自 spec 的 `recipe: ; body` 单行形式——
# 该写法非合法 just 语法（just 1.36 实测 Unknown start of token，无法解析），命令逐条保持一致。

# 三语言工具链依赖一次拉齐
bootstrap:
    go mod download && pnpm install && cargo fetch

# 按 manifest 拉取权重并校验 sha256
fetch-models:
    go run ./tools/repoctl/cmd/repoctl fetch-models --manifest models/manifests

# GO-1 gofmt 零 diff + GO-2 errcheck
lint:
    test -z "$(gofmt -l .)" && go vet ./... && errcheck ./... && pnpm lint && cargo clippy

# GO-4
test:
    go test ./... -race -count=1 && pnpm test

gate ASSET LEVEL="all":
    go run ./tools/gaterunner/cmd/gaterunner run --asset {{ASSET}} --level {{LEVEL}} --report reports/gates/{{ASSET}}.json

journeys:
    go run ./tools/journeys/cmd/journeys run --set golden --seeds 3

budgets:
    go run ./tools/budgets/cmd/budgets check --report reports/nightly/latency.json

coverage:
    go run ./tools/repoctl/cmd/repoctl coverage && go run ./tools/repoctl/cmd/repoctl agents-md check && go run ./tools/repoctl/cmd/repoctl forbidden-refs

verify:
    go run ./tools/gaterunner/cmd/gaterunner verify-configs && just coverage

nightly-local:
    just gate all g0 && just journeys && just budgets && just coverage
