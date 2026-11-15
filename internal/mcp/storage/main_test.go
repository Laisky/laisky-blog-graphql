package storage

import (
	"os"
	"testing"
	"time"
)

// TestMain shrinks the lifecycle settle delay for the in-memory S3 fixture;
// the MinIO acceptance test restores the production value.
func TestMain(m *testing.M) {
	lifecycleSettleDelay = 5 * time.Millisecond
	os.Exit(m.Run())
}

// ensureErr drops EnsureLifecycle's wrote result.
func ensureErr(_ bool, err error) error {
	return err
}
