.PHONY: install
install:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golang/protobuf/protoc-gen-go@v1.3.2

.PHONY: gen
gen:
	go get github.com/99designs/gqlgen@v0.17.17
	go get github.com/vektah/gqlparser/v2@v2.1.0
	go run github.com/99designs/gqlgen generate

.PHONY: test
test:
	@tox --recreate
	@tox

.PHONY: changelog
changelog: CHANGELOG.md
	sh ./.scripts/generate_changelog.sh

.PHONY: lint
lint:
	go mod tidy
	goimports -local laisky-blog-graphql -w .
	gofmt -s -w .
	# golangci-lint run --timeout 3m -E golint,depguard,gocognit,goconst,gofmt,misspell,exportloopref,nilerr #,gosec,lll
	golangci-lint run -c .golangci.lint.yml
