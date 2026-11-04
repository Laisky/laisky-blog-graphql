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

## Original input through ordinary structured logs and alerts

The caller selects the decoded RequireString URL before trimming, the submitted
URL, declared boolean/null output option and resolved boolean. It does not
traverse unknown arguments or serialize the HTTP request envelope, task/API key,
authorization context or process configuration. Source service, hostname,
process run and public build metadata identify the emitter; the task ID is
present only after assignment.

Full selected input is emitted through the existing log and alert path without
an application preview, replacement, filter or custom payload codec. It is not
written to an application archive or sent through a new API. Worker entries
supply the exact converter argument and observed rendered/partial response data;
the existing task identity joins the two application sources.

The application fixture is only one fidelity boundary. Existing SDK, receiver,
queue and destination size/delivery behavior must be qualified by their owners;
the application must not silently work around a loss with separate storage.
Missing old input remains an instrumentation/retention gap, not a request for the
user to reconstruct information that the logs should supply.

## Container build provenance

Docker's local directory context makes only included files available to COPY/ADD.
This release uses a local checkout (`context: .`), not a remote Git URL context.
The original .git directory is fully excluded. Explicit preparation uses a fresh
local depth-one transfer of exact HEAD and validates the complete stored object
set before publishing only public metadata under an ignored reserved directory.
The builder uses that minimal metadata for native Go stamping; no checkout auth,
config, hooks, external lookup hints, dangling objects or historical private blobs
enter the context or cache layers. Existing reserved paths cause failure rather
than reuse. Actual BuildKit scratch-export regressions include dangling and packed
historical canary blobs, then compile and run ProcessSource with the production
command to check clean/dirty identity and honest unprepared manual builds.

Go's VCS stamping requires main-module information. The named-file form produces
command-line-arguments; building the module package supplies standard revision and
modified settings. A missing outside-context worktree pointer cannot establish a
revision and is tested as an honest unknown. Fixture commands strip inherited
GIT_* overrides and ignore system/global Git configuration; no real credential
configuration is read by the fixture Git operations.

- [Docker build contexts and .dockerignore](https://docs.docker.com/build/concepts/context/)
- [Go build and -buildvcs](https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies)
- [runtime/debug.ReadBuildInfo](https://pkg.go.dev/runtime/debug#ReadBuildInfo)

These source/build controls qualify public binary provenance only. They do not
establish complete logging/alert delivery, backend retention or a production
deployment. The memory-only library/buildidentity package has no storage or
payload responsibilities.

- [Git fetch depth and tag controls](https://git-scm.com/docs/git-fetch)
