package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRawBucketLifecycleRefusesRedirects: the raw lifecycle GET carries a
// SigV4 Authorization header (access key id and a replayable signature), so
// a redirect answer fails like any other unexpected status and the signed
// request never reaches the redirect target.
func TestRawBucketLifecycleRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHits.Add(1)
			_, _ = w.Write([]byte(`<LifecycleConfiguration></LifecycleConfiguration>`))
		}))
		store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.URL.Query()["location"]; ok {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`))
				return
			}
			require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256"), "the lifecycle GET is signed")
			http.Redirect(w, r, target.URL+r.URL.RequestURI(), status)
		}))
		endpoint, err := url.Parse(store.URL)
		require.NoError(t, err)

		client, err := NewMinIOClient(MinIOConfig{Endpoint: endpoint.Host, Bucket: "bucket", AccessKey: "access-key", SecretKey: "secret-key"})
		require.NoError(t, err)
		raw, err := client.ensureClient()
		require.NoError(t, err)
		lifecycle, err := client.rawBucketLifecycle(context.Background(), raw)
		require.Error(t, err, "status %d", status)
		require.Nil(t, lifecycle)
		require.Zero(t, targetHits.Load(), "status %d: the signed request must never reach the redirect target", status)
		store.Close()
		target.Close()
	}
}
