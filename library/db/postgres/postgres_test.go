package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildDSN_SSLModeNotDisabled(t *testing.T) {
	t.Parallel()

	dsn := BuildDSN(DialInfo{
		Addr:   "db.example.com",
		DBName: "testdb",
		User:   "user",
		Pwd:    "pass",
	})

	require.NotContains(t, dsn, "sslmode=disable",
		"DSN must not use sslmode=disable; database connections should use TLS")
	require.True(t, strings.Contains(dsn, "sslmode=prefer") ||
		strings.Contains(dsn, "sslmode=require") ||
		strings.Contains(dsn, "sslmode=verify-full"),
		"DSN should use sslmode=prefer, require, or verify-full")
}

func TestBuildDSN_ContainsAllFields(t *testing.T) {
	t.Parallel()

	dsn := BuildDSN(DialInfo{
		Addr:   "myhost",
		DBName: "mydb",
		User:   "myuser",
		Pwd:    "mypass",
	})

	require.Contains(t, dsn, "host=myhost")
	require.Contains(t, dsn, "dbname=mydb")
	require.Contains(t, dsn, "user=myuser")
	require.Contains(t, dsn, "password=mypass")
}
