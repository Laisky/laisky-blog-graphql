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
