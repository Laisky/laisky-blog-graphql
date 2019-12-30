# laisky-blog-graphql

graphql backend for laisky-blog depends on gqlgen & gin.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Commitizen friendly](https://img.shields.io/badge/commitizen-friendly-brightgreen.svg)](http://commitizen.github.io/cz-cli/)
[![Go Report Card](https://goreportcard.com/badge/github.com/Laisky/laisky-blog-graphql)](https://goreportcard.com/report/github.com/Laisky/laisky-blog-graphql)
[![GoDoc](https://godoc.org/github.com/Laisky/laisky-blog-graphql?status.svg)](https://godoc.org/github.com/Laisky/laisky-blog-graphql)
[![Build Status](https://travis-ci.org/Laisky/laisky-blog-graphql.svg?branch=master)](https://travis-ci.org/Laisky/laisky-blog-graphql)
[![codecov](https://codecov.io/gh/Laisky/laisky-blog-graphql/branch/master/graph/badge.svg)](https://codecov.io/gh/Laisky/laisky-blog-graphql)


Example: <https://blog.laisky.com/graphql/ui/>

介绍: <https://blog.laisky.com/p/gqlgen/>


Run:

```sh
go run -race entrypoints/main.go \
    --debug --addr=127.0.0.1:8080 \
    --dbaddr=127.0.0.1:27017 \
    --config=./docs
```

Build:

```sh
docker build . -t ppcelery/laisky-blog-graphql:0.3.1
```
