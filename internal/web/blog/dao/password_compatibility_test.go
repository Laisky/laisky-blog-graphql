package dao

import (
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
)

// TestPasswordCompatibilityDummyRetainsPHC prevents an upgrade from weakening
// the unknown-account timing mask while real account writers remain Argon2id.
func TestPasswordCompatibilityDummyRetainsPHC(t *testing.T) {
	d := &Blog{logger: glog.Shared}
	require.Contains(t, d.dummyPasswordHash(), "$argon2id$v=19$m=65536,t=3,p=4$")
}
