// Package log is a logging package that provides functions to log messages.
package log

import (
	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var Logger gutils.LoggerItf

func init() {
	var err error
	if Logger, err = gutils.NewConsoleLoggerWithName("graphql", gutils.LoggerLevelDebug); err != nil {
		gutils.Logger.Panic("new logger", zap.Error(err))
	}
}
