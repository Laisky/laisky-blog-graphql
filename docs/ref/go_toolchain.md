# Go toolchain

The repository targets Go 1.27.0 on Debian Bookworm.

- The root module declares `go 1.27.0`, which is also the exact toolchain selected by CI.
- The local `pgvector-go` module uses the same minimum Go version.
- The production build stage uses the official `golang:1.27.0-bookworm` image.
- CI uses Go 1.27.0 directly or derives it from the root `go.mod`.

The exact Docker tag is intentional: the Bookworm suite is kept explicit so a
future Debian suite change does not silently alter the build environment.

Go 1.27 moves the HTTP/2 implementation into the standard library. The
repository pins `golang.org/x/net` to a release whose compatibility layer keeps
the transitive gRPC dependencies buildable with the new implementation.

References:

- [Go downloads](https://go.dev/dl/)
- [Go release history](https://go.dev/doc/devel/release)
- [Go 1.27 release notes](https://go.dev/doc/go1.27)
- [Go toolchains](https://go.dev/doc/toolchain)
- [x/net HTTP/2 migration notes](https://github.com/golang/net/tree/master/http2#readme)
- [Official Go image metadata](https://raw.githubusercontent.com/docker-library/official-images/master/library/golang)
