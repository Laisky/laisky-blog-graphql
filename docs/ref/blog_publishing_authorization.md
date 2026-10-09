# Authorization references

Reviewed 2026-10-09: [OWASP Authorization Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html), especially validation on every request, safe exits on denied access, and authorization regression tests.

The Blog publishing implementation applies the repository's existing server-side administrator policy at both public mutation entry points, before article and archive operations. Authentication and token display claims alone do not confer publishing authority. Content amendments retain their existing author ownership check; category curation retains its existing administrator behavior.
