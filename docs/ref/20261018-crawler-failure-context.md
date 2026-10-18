# Crawler failure context reference (2026-10-18)

The repository's existing RequestURL projection remains authoritative for admitted
target scheme/hostname and the matching worker metadata. URL.Hostname removes a
valid port and IPv6 brackets; it does not establish a new IDNA or IP spelling
policy. Both sides retain the existing lowercase/trailing-dot representation so
diagnostic provenance cannot disagree with admission.

Go's mime.ParseMediaType parses a media type or a Content-Disposition value.
Successful parsing alone therefore does not establish an HTTP MIME type. The
worker projects only a valid base type/subtype for optional content_type metadata;
omitting invalid diagnostic MIME does not accept or reject the HTTP response.

The pinned html-to-markdown/v2 converter can return empty Markdown for supplied
HTML. Conversion input/output and parsed body-text counts help distinguish that
observation from fetch/render failures. They do not establish why the historical
target produced empty_markdown, and parsed text is not a CSS visibility claim.
No failed target was retried for this investigation.

Primary references:

- [Go mime.ParseMediaType](https://pkg.go.dev/mime#ParseMediaType)
- [Go net/url.URL.Hostname](https://pkg.go.dev/net/url#URL.Hostname)
- [html-to-markdown upstream](https://github.com/JohannesKaufmann/html-to-markdown)

See [the application contract](../manual/crawler_failure_context.md) for binding,
optional observations, public-error compatibility and synthetic-test limits.

## Byte preservation and private journal primitives

The caller retains the original RequireString URL bytes before normalization.
It records the presence and actual valid boolean/null output_markdown value,
encoded as canonical JSON, separately from the resolved boolean. It does not
traverse or serialize unknown arguments or the HTTP request envelope. JSON byte
fields use a reversible base64 representation; this is an explicit private-record
codec, not an attempt to bypass alert filtering.

The standard library documents raw JSON retention, private temporary-file
creation, and file synchronization. Those primitives support atomic publication,
but do not by themselves guarantee persistent-container storage, delivery through
an asynchronous collector, or an unlimited retention period.

- [encoding/json.RawMessage](https://pkg.go.dev/encoding/json#RawMessage)
- [os.CreateTemp](https://pkg.go.dev/os#CreateTemp)
- [os.File.Sync](https://pkg.go.dev/os#File.Sync)

Test-process isolation sets only CRAWLER_FAILURE_LOG_DIR to a newly owned
os.MkdirTemp directory. Focused test leaves are explicitly chmodded to 0700;
tests do not assume t.TempDir's leaf mode or relax the production private-path
check. Cleanup is confined to these newly owned synthetic directories.

## Container build provenance

Docker's local directory context makes only included files available to COPY/ADD.
This release uses a local checkout (`context: .`), not a remote Git URL context.
The needed public Git metadata and tracked .gitignore must survive filtering for
Go's automatic VCS stamping. Checkout-private configuration and external object
lookup hints must not cross that boundary. The actual BuildKit scratch-export
regression tests that boundary with synthetic credential canaries, then compiles
and runs the real ProcessSource using the production Go command.

Go's VCS stamping requires main-module information. The named-file form produces
command-line-arguments; building the module package supplies standard revision and
modified settings. A missing outside-context worktree pointer cannot establish a
revision and is tested as an honest unknown. Fixture commands strip inherited
GIT_* overrides and ignore system/global Git configuration; no real credential
configuration is read by the fixture Git operations.

- [Docker build contexts and .dockerignore](https://docs.docker.com/build/concepts/context/)
- [Go build and -buildvcs](https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies)
- [runtime/debug.ReadBuildInfo](https://pkg.go.dev/runtime/debug#ReadBuildInfo)

These source/build controls do not qualify the persistent journal mount,
operational retention, cross-UID private export, or a production deployment.
