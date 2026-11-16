package cmd

import (
	"strings"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library/memlimit"
)

// TestSizeImageMemoryAtStartup wires the memory limit into the image
// settings the way buildUserRequestImageManager does and checks the operator
// signals: a lowered default budget (WARN), an envelope that cannot fit
// (ERROR) and a container limit without a Go soft limit (WARN).
func TestSizeImageMemoryAtStartup(t *testing.T) {
	defaults := userrequests.ImageSettings{
		PerImageMaxBytes:        userrequests.DefaultImagePerImageMaxBytes,
		MaxPerRequest:           userrequests.DefaultImageMaxPerRequest,
		MaxConcurrentCreates:    userrequests.DefaultImageMaxConcurrentCreates,
		DecodeMemoryBudgetBytes: userrequests.DefaultImageDecodeMemoryBudgetBytes,
	}
	run := func(limits memlimit.Limits) (userrequests.ImageSettings, *observer.ObservedLogs) {
		core, observed := observer.New(zapcore.DebugLevel)
		logger, err := logSDK.NewWithName("sizing", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
		require.NoError(t, err)
		return sizeImageMemory(defaults, limits, logger), observed
	}
	has := func(observed *observer.ObservedLogs, level zapcore.Level, substr string) bool {
		for _, e := range observed.All() {
			if e.Level == level && strings.Contains(e.Message, substr) {
				return true
			}
		}
		return false
	}

	got, observed := run(memlimit.Limits{})
	require.Equal(t, defaults, got)
	require.True(t, has(observed, zapcore.InfoLevel, "no process memory limit detected"))

	got, observed = run(memlimit.Limits{Container: 4 << 30, ContainerSource: memlimit.SourceCgroupV2})
	require.Less(t, got.DecodeMemoryBudgetBytes, defaults.DecodeMemoryBudgetBytes)
	require.True(t, has(observed, zapcore.WarnLevel, "decode memory budget lowered"))
	require.True(t, has(observed, zapcore.WarnLevel, "no Go soft memory limit is in effect"))

	_, observed = run(memlimit.Limits{GoSoftLimit: 1 << 30})
	require.True(t, has(observed, zapcore.ErrorLevel, "exceeds half the process memory limit"))
	require.False(t, has(observed, zapcore.WarnLevel, "no Go soft memory limit is in effect"))
}
