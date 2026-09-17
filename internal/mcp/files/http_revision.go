package files

import (
	"context"
	"net/http"
	"strings"
)

// conditionalFileHTTPContext accepts one strong If-Match token or If-None-Match: *.
// Unsupported lists, weak validators, empty fields and multiple headers fail
// closed instead of degrading to an unconditional mutation.
func conditionalFileHTTPContext(ctx context.Context, r *http.Request, auth AuthContext, project, path string, operation FileOperation) (context.Context, error) {
	matches, absent := r.Header.Values("If-Match"), r.Header.Values("If-None-Match")
	if len(matches) == 0 && len(absent) == 0 {
		return ctx, nil
	}
	bad := func() (context.Context, error) {
		return nil, NewError(ErrCodeInvalidArgument, "use one strong If-Match file version or If-None-Match: *", false)
	}
	if len(matches) > 1 || len(absent) > 1 || (len(matches) > 0 && len(absent) > 0) {
		return bad()
	}
	var p FilePreconditions
	if len(matches) == 1 {
		value := strings.TrimSpace(matches[0])
		if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
			return bad()
		}
		p.ExpectedVersion = value[1 : len(value)-1]
		if err := ValidateFileVersion(p.ExpectedVersion); err != nil {
			return nil, err
		}
	} else {
		if strings.TrimSpace(absent[0]) != "*" {
			return bad()
		}
		p.CreateOnly = true
	}
	return WithFilePreconditions(ctx, auth, project, path, operation, p)
}
