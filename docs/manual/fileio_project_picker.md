# FileIO project selection

The **Project** field on `/tools/file_io` is an editable combobox. Focus it or use
its arrow to browse existing project IDs owned by the current API key. Type to
search, click an option, or use Up/Down and Enter. Escape closes the list without
changing the input. An unlisted ID remains valid manual input: discovery failure
or an empty list does not prevent entering a new project. Selecting an ID alone
never creates a project or writes a file.

Search is case-insensitive and matches characters in order, not only a prefix:
`MCP` matches `my-chat-project`. Underscores, hyphens and dots are literal.
Project IDs retain the server's existing 1–128 ASCII letter/digit/`._-` rule.
The browser debounces searches by 200 ms and requests 50 IDs per page. **Load more
projects** retrieves another page. At 500 visible IDs, narrow the search; each new
search covers the caller's entire project namespace, not just loaded suggestions.

## Ownership and privacy

`GET /tools/file_io/api/projects?q=&after=&limit=50` uses the existing authenticated
FileIO HTTP handler. The deployment's public API prefix is applied by the shared
browser client. A successful response contains `projects`, `has_more` and, only
when another page exists, `next_cursor`. Limits are 1–100 (default 50). The cursor
is an ID used for ordering, not a tenant selector or an authorization token.

The server applies the authenticated `apikey_hash` and trusted `system_owner`
namespace before distinct selection, matching, ordering and pagination. It returns
only projects with live entries. Deleted-only and internal-system-only projects
are excluded. Supplied user IDs, owner headers and API-key hashes cannot widen
access. Two different API keys are separate namespaces, even when held by the
same human; this feature does not add account-wide enumeration. All responses
are private/no-store, including authentication errors, and vary by Authorization.

Suggestions are transient component state, not localStorage or a global cache.
Search changes, closing, locking, disconnection and account/session changes abort
requests; an additional current-request check rejects late results from transports
that ignore cancellation. The project, workspace results, drafts and edit-version
state remount together across credential, lock and session boundaries. Switching
projects retains the existing explicit unsaved-draft confirmation. A discarded
initial draft is not resurrected by returning to its old project during that session.

Form persistence uses `mcp.file_io.inputs.v2.<SHA-256 of normalized API key>` and a
whitelist of draft/path fields. Neither raw credentials, discovered project lists
nor version tokens are added to these records. The old shared `v1` record is
removed, not migrated, because its owner cannot be established. Storage failures
are nonfatal. This prevents accidental cross-credential UI restoration; it is not
encryption or protection against an attacker controlling the same browser profile.
Existing API-key history in the application provider is unchanged.

## Regression and acceptance checks

Backend: `TestProjectDiscoveryBehavior` uses real SQLite and, when configured,
PostgreSQL storage. It checks two tenants, identical project names, spoofed
identity inputs, live/system/deleted filtering, fuzzy search, literal punctuation,
keyset pagination, cancellation, invalid inputs, authenticated routing and privacy
headers. PostgreSQL uses a disposable database with pgvector and a random private
schema; never point test fixtures at production.

Frontend tests cover discovery payload validation, the actual HTTP client with
mock fetch, editable keyboard/mouse behavior, IME handling, pagination, debounce,
manual fallback, late-response cancellation, credential/lock/session resets,
legacy draft rejection and per-key persistence. Existing edit/version tests use
explicitly credential-scoped fixtures; they are not removed or replaced by picker
tests. Component tests use jsdom and mocked transports, not a deployed browser.

```sh
# Set FILEIO_TEST_POSTGRES_DSN to a disposable PostgreSQL database with pgvector.
go test -race -cover ./internal/mcp/files ./internal/mcp/auth
go vet ./internal/mcp/files ./internal/mcp/auth
cd web
pnpm install --frozen-lockfile
pnpm test
pnpm lint
pnpm build
```

The PR-only `fileio-project-picker` workflow runs these checks without deployment,
paid-provider calls or production secrets. Test source is not evidence that tests
passed: consult the exact PR head's check results and PR validation notes.
