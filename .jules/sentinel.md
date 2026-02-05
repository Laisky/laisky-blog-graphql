## 2026-02-02 - [XSS in Meta Tags]

**Vulnerability:** XSS (Cross-Site Scripting) via unsanitized strings in `BlogTwitterCard`.
**Learning:** The application was manually building HTML strings using `fmt.Sprintf` and `gutils.Dedent` for a GraphQL resolver, which bypassed standard JSON/GraphQL safety and allowed attribute breakout when injecting database or user-supplied strings.
**Prevention:** Always use `html.EscapeString` (or a proper HTML template engine) when building HTML snippets from dynamic data in Go.

## 2026-02-03 - [Reflected XSS and DoS in Registration]

**Vulnerability:** Reflected XSS in success message and lack of input length limits for registration/login.
**Learning:** Returning user-supplied data (like ) directly in an informative success message can lead to XSS if not escaped. Also, missing length limits on sensitive inputs like passwords and usernames can be exploited for DoS (CPU exhaustion during hashing) or database bloat.
**Prevention:** Always escape user-supplied strings in API responses that might be displayed in the frontend. Implement early input validation for length and format in controllers.

## 2026-02-03 - [Reflected XSS and DoS in Registration]

**Vulnerability:** Reflected XSS in `UserRegister` success message and lack of input length limits for registration/login.
**Learning:** Returning user-supplied data (like `account`) directly in an informative success message can lead to XSS if not escaped. Also, missing length limits on sensitive inputs like passwords and usernames can be exploited for DoS (CPU exhaustion during hashing) or database bloat.
**Prevention:** Always escape user-supplied strings in API responses that might be displayed in the frontend. Implement early input validation for length and format in controllers.
