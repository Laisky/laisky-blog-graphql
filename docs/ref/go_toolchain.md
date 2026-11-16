# Go toolchain

The repository targets Go 1.27.4 on Debian Bookworm.

- **Source of truth.** The root `go.mod` `go` directive (`go 1.27.4`) is the
  module and language floor and the release every build uses. A `toolchain`
  directive, if one is ever added, takes precedence.
- **Docker.** The production build stage uses the official
  `golang:1.27.4-bookworm` image.
- **CI.** Each `actions/setup-go` step either pins `go-version: '1.27.4'` or
  reads `go-version-file: go.mod`. The CodeQL job uses `go-version: 1.27`,
  which resolves to the newest 1.27 patch release.
- **Drift guard.** `.scripts/test_go_toolchain.py` fails when a tracked
  Dockerfile, a setup-go step, or this note names a different release. It runs
  in `make lint` and in the `Linter / check` workflow. When bumping Go, change
  `go.mod`, the Dockerfile, the explicit workflow pins and this note together.

Earlier 1.27 releases are not acceptable build toolchains:

- Go 1.27.3 fixes [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809) in
  `crypto/x509` (see [the crawler egress policy](../manual/crawler_egress_policy.md)).
- Go 1.27.4 (2026-11-10) fixes [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859)
  (CVE-2026-97054) in `net/http`. An HTTP/1 server can crash when a
  `MaxBytesReader`/`MaxBytesHandler` limit is hit while the handler reads the
  body and writes headers concurrently. Under Go 1.27.3,
  `govulncheck ./...` reports this vulnerability as reachable from this
  repository's code.
- Go 1.27.4 also fixes eleven `cmd/go`, `cmd/cgo`, `cmd/compile` and
  `cmd/link` vulnerabilities (GO-2026-6848 to GO-2026-6858). A malicious
  package can use them to inject flags or run code at build time. They do not
  change how the built binary behaves at runtime.

The exact Docker tag is intentional: the Bookworm suite is kept explicit so a
future Debian suite change does not silently alter the build environment.

Go 1.27 moves the HTTP/2 implementation into the standard library. The
repository pins `golang.org/x/net` to a release whose compatibility layer keeps
the transitive gRPC dependencies buildable with the new implementation.

References:

- [Go downloads](https://go.dev/dl/)
- [Go release history](https://go.dev/doc/devel/release)
- [Go 1.27.4 / 1.26.11 security announcement](https://groups.google.com/g/golang-announce/c/iY8TY0wWgbM)
- [Go 1.27 release notes](https://go.dev/doc/go1.27)
- [Go toolchains](https://go.dev/doc/toolchain)
- [actions/setup-go `go-version-file`](https://github.com/actions/setup-go#getting-go-version-from-the-gomod-file)
- [x/net HTTP/2 migration notes](https://github.com/golang/net/tree/master/http2#readme)
- [Official Go image metadata](https://raw.githubusercontent.com/docker-library/official-images/master/library/golang)
