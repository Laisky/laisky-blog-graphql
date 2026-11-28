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
// direction.
//
// It is a syntactic tripwire, not a proof. It finds credential reads
// through Get/GetHeader/Values/Query/DefaultQuery/GetQuery/FormValue/
// PostFormValue calls and map indexing, with the name spelled as a string
// literal or a string constant (same or imported package); one-api routes
// spelled in a string literal or as separate "api", "token"/"user"/...
// segments of one call (url.JoinPath); identity literals; and
// ctxkeys.AuthContext writes. It cannot see a credential arriving as a
// GraphQL argument, a JSON field or a chat message, or a name computed at
// run time; those entrypoints are kept in the architecture table by review.
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
		"internal/web/sso_code.go":                        "SSO code exchange and userinfo: SSO bearer verified by signature (not an API key)",
		"internal/web/generated.go":                       "gqlgen decodes api_key arguments: ValidateOneapiAPIKey (Authenticator.Balance), retired GeneralAddLLMStormTask (ignored)",
		"cmd/api.go":                                      "platform search-engine keys read from configuration, not caller credentials",
	}
	// oneapiRESTCallers build a request to one-api's per-IP-limited /api group.
	oneapiRESTCallers = map[string]string{
		"internal/mcp/auth/oneapi_validator.go": "GET /api/token/balance, admitted by UpstreamGate and recorded in the ledger",
		"library/billing/oneapi/oneapi.go":      "POST /api/token/consume, recorded in oneapi.CallLedger",
	}
	// identityBuilders construct an identity value directly instead of
	// receiving a verified one from the authenticator.
	identityBuilders = map[string]string{
		"internal/mcp/auth/auth.go":          "DeriveFromAPIKey: the unverified candidate that Authenticate verifies",
		"internal/mcp/askuser/auth.go":       "NewTelegramAnswerer: the one key-less delegated principal",
		"internal/mcp/files/auth_context.go": "AuthFromVerified: the only conversion from a verified context",
		"internal/mcp/files/system_fs.go":    "system-owned files, never a caller tenant",
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

// parsedFile is one non-test Go file of the module.
type parsedFile struct {
	rel  string
	file *ast.File
}

// scanModule parses every non-test Go file of the module, collects the
// string constants of every package, then scans each file.
func scanModule(t *testing.T, root string) inventoryFindings {
	t.Helper()
	found := inventoryFindings{
		readers: map[string][]string{}, oneapi: map[string][]string{},
		builders: map[string][]string{}, writers: map[string][]string{},
	}
	fset := token.NewFileSet()
	var files []parsedFile
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
		files = append(files, parsedFile{rel: filepath.ToSlash(rel), file: file})
		return nil
	})
	require.NoError(t, err)

	// constants maps a package directory (module-relative) to its string constants.
	constants := map[string]map[string]string{}
	for _, parsed := range files {
		dir := filepath.ToSlash(filepath.Dir(parsed.rel))
		if constants[dir] == nil {
			constants[dir] = map[string]string{}
		}
		collectStringConstants(parsed.file, constants[dir])
	}
	modulePath := modulePathOf(t, root)
	for _, parsed := range files {
		scanFile(fset, parsed.rel, parsed.file, constants, modulePath, &found)
	}
	return found
}

// modulePathOf reads the module path from go.mod.
func modulePathOf(t *testing.T, root string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	for line := range strings.SplitSeq(string(content), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(path)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// collectStringConstants records the package-level string constants of file.
func collectStringConstants(file *ast.File, into map[string]string) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if index >= len(value.Values) {
					break
				}
				if literal, ok := value.Values[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
					if unquoted, err := strconv.Unquote(literal.Value); err == nil {
						into[name.Name] = unquoted
					}
				}
			}
		}
	}
}

// scanFile records the inventory sites of one file.
func scanFile(fset *token.FileSet, rel string, file *ast.File, constants map[string]map[string]string, modulePath string, found *inventoryFindings) {
	imports := importNames(file)
	ownDir := filepath.ToSlash(filepath.Dir(rel))
	ownPackage := "/" + ownDir
	resolve := func(expr ast.Expr) (string, bool) {
		switch typed := expr.(type) {
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				return "", false
			}
			value, err := strconv.Unquote(typed.Value)
			return value, err == nil
		case *ast.Ident:
			value, ok := constants[ownDir][typed.Name]
			return value, ok
		case *ast.SelectorExpr:
			pkg, ok := typed.X.(*ast.Ident)
			if !ok {
				return "", false
			}
			dir, ok := strings.CutPrefix(imports[pkg.Name], modulePath+"/")
			if !ok {
				return "", false
			}
			value, ok := constants[dir][typed.Sel.Name]
			return value, ok
		}
		return "", false
	}
	record := func(into map[string][]string, node ast.Node) {
		into[rel] = append(into[rel], strconv.Itoa(fset.Position(node.Pos()).Line))
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if isCredentialRead(node, resolve) {
			record(found.readers, node)
		}
		switch typed := node.(type) {
		case *ast.CallExpr:
			if isAuthContextWrite(typed, imports) {
				record(found.writers, typed)
			}
			if joinsOneAPIRoute(typed, resolve) {
				record(found.oneapi, typed)
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

// credentialReadMethods are the request accessors that can return a credential.
var credentialReadMethods = []string{"Get", "GetHeader", "Values", "Query", "DefaultQuery", "GetQuery", "FormValue", "PostFormValue"}

// isCredentialName reports whether a header, query or form name carries a credential.
func isCredentialName(name string) bool {
	return strings.EqualFold(name, "Authorization") || slices.Contains(credentialQueryKeys, strings.ToLower(name))
}

// isCredentialRead matches accessor calls such as x.Get("Authorization") or
// c.Query(apiKeyParam) and map indexing such as r.Header["Authorization"].
func isCredentialRead(node ast.Node, resolve func(ast.Expr) (string, bool)) bool {
	switch typed := node.(type) {
	case *ast.CallExpr:
		selector, ok := typed.Fun.(*ast.SelectorExpr)
		if !ok || len(typed.Args) == 0 || !slices.Contains(credentialReadMethods, selector.Sel.Name) {
			return false
		}
		name, ok := resolve(typed.Args[0])
		return ok && isCredentialName(name)
	case *ast.IndexExpr:
		name, ok := resolve(typed.Index)
		return ok && isCredentialName(name)
	}
	return false
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

// oneapiRouteGroups are the one-api /api route groups this server could call.
var oneapiRouteGroups = []string{"token", "user", "channel", "log"}

// joinsOneAPIRoute matches a call that assembles a one-api route from
// separate segments, such as url.JoinPath(base, "api", "token", "consume").
func joinsOneAPIRoute(call *ast.CallExpr, resolve func(ast.Expr) (string, bool)) bool {
	for index := 0; index+1 < len(call.Args); index++ {
		first, ok := resolve(call.Args[index])
		if !ok || strings.Trim(first, "/") != "api" {
			continue
		}
		if next, ok := resolve(call.Args[index+1]); ok && slices.Contains(oneapiRouteGroups, strings.Trim(next, "/")) {
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

// TestInventoryScannerSeesIndirectSpellings covers the spellings an
// independent review found the first scanner missed: a header name in a
// constant (same or imported package), map indexing, query and form
// accessors, and a one-api route assembled from path segments.
func TestInventoryScannerSeesIndirectSpellings(t *testing.T) {
	const source = `package x

import (
	"net/http"
	"net/url"

	"example.com/m/internal/names"
)

const header = "Authorization"

func reads(r *http.Request, c interface{ Query(string) string }) {
	_ = r.Header.Get(header)
	_ = r.Header.Get(names.Auth)
	_ = r.Header["Authorization"]
	_ = r.URL.Query()["apikey"]
	_ = r.FormValue("api_key")
	_ = c.Query("APIKEY")
	_, _ = url.JoinPath("https://oneapi.example.com", "api", "token", "consume")
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", source, parser.SkipObjectResolution)
	require.NoError(t, err)
	constants := map[string]map[string]string{"internal/x": {}, "internal/names": {"Auth": "Authorization"}}
	collectStringConstants(file, constants["internal/x"])
	found := inventoryFindings{readers: map[string][]string{}, oneapi: map[string][]string{}, builders: map[string][]string{}, writers: map[string][]string{}}
	scanFile(fset, "internal/x/x.go", file, constants, "example.com/m", &found)
	require.Len(t, found.readers["internal/x/x.go"], 6)
	require.Len(t, found.oneapi["internal/x/x.go"], 1)
}
