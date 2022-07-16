// Package log is a logging package that provides functions to log messages.
package log

import (
	"github.com/Laisky/go-utils/v2/log"
	glog "github.com/Laisky/go-utils/v2/log"
	"github.com/Laisky/zap"
)

var Logger glog.Logger

func init() {
	var err error
	if Logger, err = glog.NewConsoleWithName("graphql", glog.LevelDebug); err != nil {
		log.Shared.Panic("new logger", zap.Error(err))
	}
}
