## 2026-02-02 - [XSS in Meta Tags]

**Vulnerability:** XSS (Cross-Site Scripting) via unsanitized strings in `BlogTwitterCard`.
**Learning:** The application was manually building HTML strings using `fmt.Sprintf` and `gutils.Dedent` for a GraphQL resolver, which bypassed standard JSON/GraphQL safety and allowed attribute breakout when injecting database or user-supplied strings.
**Prevention:** Always use `html.EscapeString` (or a proper HTML template engine) when building HTML snippets from dynamic data in Go.
