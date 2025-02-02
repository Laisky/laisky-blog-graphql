.PHONY: install
install:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install google.golang.org/protobuf/protoc-gen-go@latest

.PHONY: gen
gen:
	go run github.com/99designs/gqlgen@v0.17.64 generate

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
