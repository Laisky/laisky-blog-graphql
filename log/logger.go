package log

import (
	"sync"

	"github.com/Laisky/go-utils"
)

var (
	lock   sync.RWMutex
	logger = utils.Logger
)

func GetLog() *utils.LoggerType {
	lock.RLock()
	defer lock.RUnlock()
	return logger
}

func SetLog(log *utils.LoggerType) {
	lock.Lock()
	defer lock.Unlock()
	logger = log
}
