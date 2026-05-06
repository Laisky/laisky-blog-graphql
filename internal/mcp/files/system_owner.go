package files

import "context"

// systemOwnerKey is a private context key used by SystemFS to override the
// implicit system_owner predicate that user-facing methods use ("").
type systemOwnerKey struct{}

// contextWithSystemOwner returns a derived context bound to the supplied owner.
// The empty string is the user namespace; non-empty values are restricted system
// namespaces such as "pageindex" (proposal §2.6.3, §2.8).
func contextWithSystemOwner(ctx context.Context, owner string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, systemOwnerKey{}, owner)
}

// systemOwnerFromContext returns the owner stored on the context. It defaults to
// the empty string so every existing caller keeps operating in the user namespace.
func systemOwnerFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(systemOwnerKey{}).(string)
	return v
}
