# laisky-blog-graphql

graphql backend for laisky-blog


Run:

```sh
go run -race entrypoints/main.go --debug --addr=127.0.0.1:8080 --dbaddr=127.0.0.1:27017
```

Build:

```sh
docker build . -t ppcelery/laisky-blog-graphql:0.1
```
