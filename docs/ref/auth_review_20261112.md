# Authentication review follow-up references (2026-11-12)

- NIST SP 800-63B-4, https://pages.nist.gov/800-63-4/sp800-63b.html (retrieved 2026-11-12): authentication retry accounting belongs to the authenticator/account. The bounded local tracker resets all learned aliases on successful token issuance; concurrent reservations remain. This is not a claim of multi-replica enforcement.
- Cloudflare Turnstile get-started guide, https://developers.cloudflare.com/turnstile/get-started/ (updated May 5, 2026; retrieved 2026-11-12): the widget public site key and server validation secret form a pair. Both browser challenge rendering and server verification are required. Partial configured pairs are rejected rather than silently weakening account escalation.
- Owner direction on 2026-11-12 explicitly retires legacy BlogLogin. This intentional API removal is documented in the SSO manual; the modern flows and stored account/token formats are retained.
