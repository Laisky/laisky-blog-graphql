package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

func TestValidateOneAPITokenSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer good", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"token_status":1}`)
	}))
	defer srv.Close()

	originalBase := oneapi.BillingAPI
	oneapi.BillingAPI = srv.URL
	defer func() { oneapi.BillingAPI = originalBase }()

	originalClient := defaultHTTPClient
	defaultHTTPClient = srv.Client()
	defer func() { defaultHTTPClient = originalClient }()

	tg := &Telegram{}
	token, err := tg.validateOneAPIToken(context.Background(), "good")
	require.NoError(t, err)
	require.Equal(t, "good", token)
}

func TestValidateOneAPITokenFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "unauthorized")
	}))
	defer srv.Close()

	originalBase := oneapi.BillingAPI
	oneapi.BillingAPI = srv.URL
	defer func() { oneapi.BillingAPI = originalBase }()

	originalClient := defaultHTTPClient
	defaultHTTPClient = srv.Client()
	defer func() { defaultHTTPClient = originalClient }()

	tg := &Telegram{}
	_, err := tg.validateOneAPIToken(context.Background(), "bad")
	require.Error(t, err)
}
