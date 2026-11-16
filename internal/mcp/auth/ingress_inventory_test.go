package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The registries below are the credential ingress and one-api call
// inventories of docs/arch/mcp_auth.md (sections 3 and 4). Each entry names
// a non-test file and how the credential it reads, or the one-api route it
// calls, is authenticated or budgeted. TestIngressInventory scans the whole
// module and fails when the code and a registry disagree in either
// direction, so a new entrypoint cannot read a credential, call one-api or
// build an identity without being reviewed and documented.
var (
	// credentialReaders read an inbound credential: an Authorization header
	// or the MCP query key.
	credentialReaders = map[string]string{
		"internal/mcp/server_auth.go":                     "MCP transport: withBearerAuthentication → Authenticate",
		"internal/mcp/auth/http_middleware.go":            "console APIs: HTTPMiddleware → Authenticate",
		"internal/mcp/askuser/http.go":                    "ask_user console: ParseAuthorizationFromContext → FromContextOrHeader",
		"internal/mcp/calllog/http.go":                    "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/mcp/files/http.go":                      "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/mcp/files/http_projects.go":             "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/mcp/userrequests/http.go":               "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/mcp/userrequests/hold_http.go":          "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/mcp/userrequests/preferences_http.go":   "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/mcp/userrequests/saved_command_http.go": "behind HTTPMiddleware; ParseAuthorizationFromContext reads the verified context",
		"internal/library/fileio/resolver.go":             "GraphQL FileIO/Memory: FromContextOrHeader → files.AuthFromVerified",
		"internal/library/rag/resolver.go":                "GraphQL ExtractKeyInfo: FromContextOrHeader",
		"internal/library/search/service.go":              "GraphQL WebFetch/WebSearch: FromContextOrHeader",
		"internal/web/arweave/controller/arweave.go":      "GraphQL ArweaveUpload: blog JWT, else FromContextOrHeader before the consume",
		"internal/web/blog/service/posts.go":              "blog SSO JWT bearer, verified by signature (not an API key)",
	}
	// oneapiRESTCallers build a request to one-api's per-IP-limited /api group.
	oneapiRESTCallers = map[string]string{
		"internal/mcp/auth/oneapi_validator.go": "GET /api/token/balance, admitted by UpstreamGate and recorded in the ledger",
		"library/billing/oneapi/oneapi.go":      "POST /api/token/consume, recorded in oneapi.CallLedger",
	}
	// identityBuilders construct an identity value directly instead of
	// receiving a verified one from the authenticator.
	identityBuilders = map[string]string{
		"internal/mcp/auth/auth.go":                      "DeriveFromAPIKey: the unverified candidate that Authenticate verifies",
		"internal/mcp/askuser/auth.go":                   "NewTelegramAnswerer: the one key-less delegated principal",
		"internal/mcp/files/auth_context.go":             "AuthFromVerified: the only conversion from a verified context",
		"internal/mcp/files/system_fs.go":                "system-owned files, never a caller tenant",
		"internal/mcp/files/service_mutation_helpers.go": "rescoping an already authorized identity",
		// Offline tools: synthetic tenants for benchmarks and plugin
		// conformance runs (cmd/eval-plugin, cmd/memory-bench); no request
		// reaches them.
		"internal/mcp/memory/benchmark/backend.go":          "offline memory benchmark",
		"internal/mcp/memory/conformance/eval/ops_probe.go": "offline plugin conformance probe",
		"internal/mcp/memory/conformance/eval/redteam.go":   "offline plugin conformance red-team cases",
		"internal/mcp/memory/conformance/eval/runner.go":    "offline plugin conformance runner",
	}
	// authContextWriters store a value under ctxkeys.AuthContext.
	authContextWriters = map[string]string{
		"internal/mcp/auth/auth.go": "WithContext: stores the authenticator's verified result",
	}
)

// credentialQueryKeys are the query parameters the MCP transport accepts a key from.
var credentialQueryKeys = []string{"apikey", "api_key"}

// oneapiRESTRoutePrefixes identify a one-api /api route in a string literal.
var oneapiRESTRoutePrefixes = []string{"/api/token/", "/api/user/", "/api/channel/", "/api/log/"}

// identityTypes are the identity types whose direct construction is
// registered: package import path suffix → type name.
var identityTypes = map[string]string{
	"/internal/mcp/auth":    "Context",
	"/internal/mcp/askuser": "AuthorizationContext",
	"/internal/mcp/files":   "AuthContext",
}

// inventoryFindings is what one scan of the module found, by category.
type inventoryFindings struct {
	readers, oneapi, builders, writers map[string][]string
}

// TestIngressInventory keeps the credential and one-api inventories of
// docs/arch/mcp_auth.md exact (invariants I1–I3 and I8).
func TestIngressInventory(t *testing.T) {
	root := moduleRoot(t)
	found := scanModule(t, root)

	compare := func(name string, registry map[string]string, got map[string][]string) {
		t.Helper()
		for file, sites := range got {
			if _, ok := registry[file]; !ok {
				t.Errorf("%s: unregistered %s at %v; authenticate it through internal/mcp/auth, "+
					"then register it here and in docs/arch/mcp_auth.md", file, name, sites)
			}
		}
		for file := range registry {
			if _, ok := got[file]; !ok {
				t.Errorf("%s: registered as a %s but none found; remove the stale entry here and in docs/arch/mcp_auth.md", file, name)
			}
		}
	}
	compare("credential reader", credentialReaders, found.readers)
	compare("one-api REST call", oneapiRESTCallers, found.oneapi)
	compare("identity construction", identityBuilders, found.builders)
	compare("ctxkeys.AuthContext writer", authContextWriters, found.writers)
}

// moduleRoot walks up from the test's directory to go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found")
		dir = parent
	}
}

// scanModule parses every non-test Go file of the module once.
func scanModule(t *testing.T, root string) inventoryFindings {
	t.Helper()
	found := inventoryFindings{
		readers: map[string][]string{}, oneapi: map[string][]string{},
		builders: map[string][]string{}, writers: map[string][]string{},
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch {
			case path == filepath.Join(root, "web"): // the frontend
				return filepath.SkipDir
			case slices.Contains([]string{".git", "node_modules", "vendor", "testdata"}, entry.Name()):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanFile(fset, filepath.ToSlash(rel), file, &found)
		return nil
	})
	require.NoError(t, err)
	return found
}

// scanFile records the inventory sites of one file.
func scanFile(fset *token.FileSet, rel string, file *ast.File, found *inventoryFindings) {
	imports := importNames(file)
	ownPackage := "/" + filepath.ToSlash(filepath.Dir(rel))
	record := func(into map[string][]string, node ast.Node) {
		into[rel] = append(into[rel], strconv.Itoa(fset.Position(node.Pos()).Line))
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			if isCredentialRead(typed) {
				record(found.readers, typed)
			}
			if isAuthContextWrite(typed, imports) {
				record(found.writers, typed)
			}
		case *ast.BasicLit:
			if typed.Kind == token.STRING && namesOneAPIRoute(typed.Value) {
				record(found.oneapi, typed)
			}
		case *ast.CompositeLit:
			// A zero value (T{}) carries no identity; only populated literals count.
			if len(typed.Elts) > 0 && buildsIdentity(typed.Type, imports, ownPackage) {
				record(found.builders, typed)
			}
		}
		return true
	})
}

// importNames maps each import's local name to its path.
func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = path
	}
	return names
}

// isCredentialRead matches x.Get/GetHeader/Values("Authorization") and
// x.Get("apikey"|"api_key"|"APIKEY").
func isCredentialRead(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 || !slices.Contains([]string{"Get", "GetHeader", "Values"}, selector.Sel.Name) {
		return false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return false
	}
	return strings.EqualFold(value, "Authorization") || slices.Contains(credentialQueryKeys, strings.ToLower(value))
}

// isAuthContextWrite matches context.WithValue(ctx, ctxkeys.AuthContext, ...).
func isAuthContextWrite(call *ast.CallExpr, imports map[string]string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "WithValue" || len(call.Args) < 2 {
		return false
	}
	key, ok := call.Args[1].(*ast.SelectorExpr)
	if !ok || key.Sel.Name != "AuthContext" {
		return false
	}
	pkg, ok := key.X.(*ast.Ident)
	return ok && strings.HasSuffix(imports[pkg.Name], "/internal/mcp/ctxkeys")
}

// namesOneAPIRoute matches a string literal that contains a one-api /api route.
func namesOneAPIRoute(quoted string) bool {
	value, err := strconv.Unquote(quoted)
	if err != nil {
		return false
	}
	for _, prefix := range oneapiRESTRoutePrefixes {
		if strings.Contains(value, prefix) {
			return true
		}
	}
	return false
}

// buildsIdentity matches a composite literal of a registered identity type,
// qualified (pkg.Type{}) or, inside its own package, unqualified (Type{}).
func buildsIdentity(expr ast.Expr, imports map[string]string, ownPackage string) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := typed.X.(*ast.Ident)
		if !ok {
			return false
		}
		for suffix, name := range identityTypes {
			if typed.Sel.Name == name && strings.HasSuffix(imports[pkg.Name], suffix) {
				return true
			}
		}
	case *ast.Ident:
		for suffix, name := range identityTypes {
			if typed.Name == name && strings.HasSuffix(ownPackage, suffix) {
				return true
			}
		}
	}
	return false
}
