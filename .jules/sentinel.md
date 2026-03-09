## 2026-02-02 - [XSS in Meta Tags]

**Vulnerability:** XSS (Cross-Site Scripting) via unsanitized strings in `BlogTwitterCard`.
**Learning:** The application was manually building HTML strings using `fmt.Sprintf` and `gutils.Dedent` for a GraphQL resolver, which bypassed standard JSON/GraphQL safety and allowed attribute breakout when injecting database or user-supplied strings.
**Prevention:** Always use `html.EscapeString` (or a proper HTML template engine) when building HTML snippets from dynamic data in Go.

## 2026-02-03 - [Reflected XSS and DoS in Registration]

**Vulnerability:** Reflected XSS in success message and lack of input length limits for registration/login.
**Learning:** Returning user-supplied data (like ) directly in an informative success message can lead to XSS if not escaped. Also, missing length limits on sensitive inputs like passwords and usernames can be exploited for DoS (CPU exhaustion during hashing) or database bloat.
**Prevention:** Always escape user-supplied strings in API responses that might be displayed in the frontend. Implement early input validation for length and format in controllers.

## 2026-03-09 - [Insecure CORS IP Validation & Missing Security Headers]

**Vulnerability:** Insecure CORS validation using `strings.HasPrefix` for IP addresses allowed bypass via domains like `127.0.0.1.attacker.com`. Also, the application lacked standard security headers (HSTS, CSP, etc.) protecting against common web attacks.
**Learning:** Simple prefix matching for origin validation is dangerous because DNS hostnames can contain IP-like prefixes. Proper IP parsing and validation (`net.ParseIP`) must be used for loopback or private network checks. Global security headers provide a baseline of protection against clickjacking, sniffing, and XSS.
**Prevention:** Use `net.ParseIP` and built-in methods (`IsLoopback`, `IsPrivate`) for network-based origin validation. Always include a global middleware to set standard security headers (`X-Frame-Options`, `X-Content-Type-Options`, etc.).
