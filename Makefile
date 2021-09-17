install:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.27.0
	go get golang.org/x/tools/cmd/goimports
	go get -u github.com/golang/protobuf/protoc-gen-go@v1.3.2
	go get -u github.com/go-bindata/go-bindata

gen:
	go get github.com/vektah/gqlparser/v2@v2.1.0
	go get github.com/99designs/gqlgen
	go run github.com/99designs/gqlgen

test:
	@tox --recreate
	@tox

changelog: CHANGELOG.md
	sh ./.scripts/generate_changelog.sh

lint:
	go mod tidy
	goimports -local laisky-blog-graphql -w .
	gofmt -s -w .
	# golangci-lint run --timeout 3m -E golint,depguard,gocognit,goconst,gofmt,misspell,exportloopref,nilerr #,gosec,lll
	golangci-lint run -c .golangci.lint.yml
