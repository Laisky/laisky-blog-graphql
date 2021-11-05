package config

import (
	"path/filepath"

	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

func LoadFromFile(cfgPath string) {
	gutils.Settings.Set("cfg_dir", filepath.Dir(cfgPath))
	if err := gutils.Settings.LoadFromFile(cfgPath); err != nil {
		log.Logger.Panic("load configuration",
			zap.Error(err),
			zap.String("config", cfgPath))
	}

	log.Logger.Info("load configuration",
		zap.String("config", cfgPath))
}

func LoadTest() {
	LoadFromFile("/opt/configs/laisky-blog-graphql/settings.yml")
}
