bootstrap:            ; go mod download && pnpm install && cargo fetch
fetch-models:         ; go run ./tools/repoctl/cmd/repoctl fetch-models --manifest models/manifests   # 按 manifest 拉取权重并校验 sha256
lint:                 ; test -z "$(gofmt -l .)" && go vet ./... && errcheck ./... && pnpm lint && cargo clippy   # GO-1 gofmt 零 diff + GO-2 errcheck
test:                 ; go test ./... -race -count=1 && pnpm test   # GO-4
gate ASSET LEVEL="all":; go run ./tools/gaterunner/cmd/gaterunner run --asset {{ASSET}} --level {{LEVEL}} --report reports/gates/{{ASSET}}.json
journeys:             ; go run ./tools/journeys/cmd/journeys run --set golden --seeds 3
budgets:              ; go run ./tools/budgets/cmd/budgets check --report reports/nightly/latency.json
coverage:             ; go run ./tools/repoctl/cmd/repoctl coverage && go run ./tools/repoctl/cmd/repoctl agents-md check && go run ./tools/repoctl/cmd/repoctl forbidden-refs
verify:               ; go run ./tools/gaterunner/cmd/gaterunner verify-configs && just coverage
nightly-local:        ; just gate all g0 && just journeys && just budgets && just coverage
