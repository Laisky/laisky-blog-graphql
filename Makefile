.PHONY: install
install:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install google.golang.org/protobuf/protoc-gen-go@latest

.PHONY: gen
gen:
	GQLGEN_DEBUG=1 GQLGEN_TRACE=1 go run github.com/99designs/gqlgen@v0.17.84 generate

.PHONY: test
test:
	@tox --recreate
	@tox

.PHONY: changelog
changelog: CHANGELOG.md
	sh ./.scripts/generate_changelog.sh

.PHONY: lint
lint:
	goimports -local github.com/Laisky/laisky-blog-graphql -w .
	go mod tidy
	gofmt -s -w .
	go vet ./...
	golangci-lint run -c .golangci.lint.yml
	govulncheck ./...
	$(MAKE) lint-pure-go
	$(MAKE) lint-system-owner

.PHONY: lint-pure-go
lint-pure-go:
	./.scripts/check_pure_go.sh

.PHONY: lint-system-owner
lint-system-owner:
	./.scripts/check_system_owner.sh

.PHONY: build
build:
	cd ./web && pnpm run build

.PHONY: dev
dev:
	@echo "Starting frontend dev server..."
	@echo "By default, the dev server proxies backend requests to http://localhost:17800"
	@echo "You can override this by setting VITE_BACKEND_URL"
	@echo "To have the backend proxy to this dev server, start the backend with VITE_DEV_URL=http://localhost:5173"
	cd ./web && pnpm run dev  --host 0.0.0.0

.PHONY: format
format:
	npx prettier --write .

.PHONY: eval-plugin
eval-plugin:
	@if [ -z "$(PLUGIN)" ]; then echo "PLUGIN required, e.g. make eval-plugin PLUGIN=rag"; exit 2; fi
	go run ./cmd/eval-plugin \
		--plugin=$(PLUGIN) \
		--golden=tests/eval/golden \
		--git-sha=$$(git rev-parse --short HEAD)

.PHONY: eval-baseline-rag
eval-baseline-rag:
	go run ./cmd/eval-plugin \
		--plugin=rag \
		--golden=tests/eval/golden \
		--baseline \
		--git-sha=$$(git rev-parse --short HEAD)

.PHONY: memory-bench-test
memory-bench-test:
	go test -v -race -cover ./internal/mcp/memory/benchmark ./cmd/memory-bench
	go vet ./internal/mcp/memory/benchmark ./cmd/memory-bench

.PHONY: memory-bench-local
memory-bench-local:
	@if [ -z "$(PLUGIN)" ]; then echo "PLUGIN required, e.g. make memory-bench-local PLUGIN=rag"; exit 2; fi
	go run ./cmd/memory-bench \
		--backend=local \
		--plugin=$(PLUGIN) \
		--dataset=tests/eval/memory_bench_smoke.jsonl \
		--format=canonical \
		--top-k=5 \
		--concurrency=1 \
		--warmup=1 \
		--repetitions=3 \
		--seed=42 \
		--index-timeout=30s \
		--poll-interval=25ms \
		--out=docs/eval/runs/local/$(PLUGIN)

.PHONY: memory-bench-current
memory-bench-current:
	$(MAKE) memory-bench-local PLUGIN=rag
	$(MAKE) memory-bench-local PLUGIN=pageindex

.PHONY: memory-bench-live
memory-bench-live:
	@if [ -z "$(PLUGIN)" ]; then echo "PLUGIN required, e.g. make memory-bench-live PLUGIN=rag"; exit 2; fi
	@if [ -z "$$MCP_ENDPOINT" ]; then echo "MCP_ENDPOINT is required"; exit 2; fi
	go run ./cmd/memory-bench \
		--backend=mcp \
		--plugin=$(PLUGIN) \
		--dataset=tests/eval/memory_bench_smoke.jsonl \
		--format=canonical \
		--top-k=5 \
		--concurrency=2 \
		--warmup=2 \
		--repetitions=10 \
		--seed=42 \
		--index-timeout=120s \
		--poll-interval=500ms
