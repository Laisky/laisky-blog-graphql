// Package log is a logging package that provides functions to log messages.
package log

import (
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

// Logger names are part of the alert boundary contract: alert scopes are matched by exact full name,
// so producers name their loggers with these constants instead of repeating the literals.
const (
	// RootLoggerName names the process-wide base logger created by this package.
	RootLoggerName = "graphql"
	// ServiceLoggerScope is the child scope cmd setupLogger appends after it attaches alert delivery.
	ServiceLoggerScope = "laisky-graphql"

	// serviceLoggerName is the full production prefix every reviewed alert scope is anchored to.
	serviceLoggerName = RootLoggerName + "." + ServiceLoggerScope
)

var Logger logSDK.Logger

func init() {
	var err error
	if Logger, err = logSDK.NewConsoleWithName(RootLoggerName, logSDK.LevelInfo, privateOTLPOption()); err != nil {
		logSDK.Shared.Panic("new logger", zap.Error(err))
	}
}
