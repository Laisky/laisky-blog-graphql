package auth

import (
	"encoding/json"
	"net/http"
)

// HTTPMiddleware enforces authorization and injects canonical auth context into request contexts.
func HTTPMiddleware(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCtx, err := ParseAuthorizationContext(r.Header.Get("Authorization"))
		if err != nil {
			writeUnauthorized(w, err.Error())
			return
		}

		next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), authCtx)))
	})
}

// writeUnauthorized writes a standardized 401 body for auth middleware failures.
func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}
