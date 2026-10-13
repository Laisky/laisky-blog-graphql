package search

import (
	"bytes"
	"context"
	"testing"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

func TestManagerLoggerPrivacyAndCorrelation(t *testing.T) {
	var output bytes.Buffer
	base, err := logSDK.NewWithName("synthetic", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zapcore.DebugLevel)
	}))
	require.NoError(t, err)
	unsafe := base.With(zap.String("remote_addr", "private-address-canary"), zap.String("path", "private-path-canary"), zap.String("authorization", "private-auth-canary"))
	unsafe.Debug("unsafe logger positive control")
	require.Contains(t, output.String(), "private-address-canary", "prove the observer sees inherited fields before resetting it")
	output.Reset()
	ctx := context.WithValue(context.Background(), gutils.CtxKey("gmw-logger"), unsafe)
	id := "0199c118-4229-7000-8000-000000000001"
	ctx = diagnostics.WithRequestID(ctx, id)
	ctx = diagnostics.WithLogger(ctx, base)
	manager, err := NewManager([][]Engine{{&testEngine{name: "private-engine-canary", err: errors.New("private-error-canary")}}}, WithLogger(base))
	require.NoError(t, err)
	_, err = manager.Search(ctx, "private-query-canary")
	require.Error(t, err)
	require.NotContains(t, output.String(), "canary")
	require.Contains(t, output.String(), `"category":"unknown"`)
	require.Contains(t, output.String(), id)
}
