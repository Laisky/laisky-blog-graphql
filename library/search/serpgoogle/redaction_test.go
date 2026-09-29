package serpgoogle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gmw "github.com/Laisky/gin-middlewares/v7"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"
)

// TestSearchNeverExposesProviderKey covers requests, echoed error bodies and transport failures.
func TestSearchNeverExposesProviderKey(t *testing.T) {
	const key = "synthetic-provider-key+not-a-secret"
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			core, observed := observer.New(zap.DebugLevel)
			logger, err := glog.NewWithName("test", glog.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Query().Get("api_key") != key {
					t.Error("provider key not forwarded")
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "echo: " + key})
			}))
			defer server.Close()
			engine := NewSearchEngine(key, WithEndpoint(server.URL), WithHTTPClient(server.Client()))
			_, err = engine.Search(gmw.SetLogger(context.Background(), logger), "query")
			require.Error(t, err)
			require.NotContains(t, err.Error(), key)
			require.NotContains(t, err.Error(), url.QueryEscape(key))
			logged, err := json.Marshal(observed.All())
			require.NoError(t, err)
			require.NotContains(t, string(logged), key)
			require.NotContains(t, string(logged), url.QueryEscape(key))
			require.NotEmpty(t, observed.All(), "logging must not be disabled to hide a leak")
		})
	}
	engine := NewSearchEngine(key, WithEndpoint("https://example.test/search"), WithHTTPClient(&http.Client{
		Transport: redactionTransport(func(req *http.Request) (*http.Response, error) { return nil, context.Canceled }),
	}))
	_, err := engine.Search(context.Background(), "query")
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, strings.Contains(err.Error(), key))
	require.False(t, strings.Contains(err.Error(), url.QueryEscape(key)))
	require.True(t, errors.Is(err, context.Canceled))
}

type redactionTransport func(*http.Request) (*http.Response, error)

func (f redactionTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
