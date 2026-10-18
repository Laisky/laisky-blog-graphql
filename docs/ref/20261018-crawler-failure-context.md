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
