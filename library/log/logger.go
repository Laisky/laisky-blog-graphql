// Package log is a logging package that provides functions to log messages.
package log

import (
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

var Logger logSDK.Logger

func init() {
	var err error
	if Logger, err = logSDK.NewConsoleWithName("graphql", logSDK.LevelInfo); err != nil {
		logSDK.Shared.Panic("new logger", zap.Error(err))
	}
}
