// Package config contains all the configuration used in the application.
package config

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

const (
	testConfigEnvKey      = "LAISKY_BLOG_TEST_CONFIG"
	testConfigDefaultPath = "../../docs/example/config/settings.yml"
)

// LoadFromFile loads configuration from cfgPath and stores the base directory for later lookups.
func LoadFromFile(cfgPath string) {
	gconfig.Shared.Set("cfg_dir", filepath.Dir(cfgPath))
	if err := gconfig.Shared.LoadFromFile(cfgPath); err != nil {
		log.Logger.Panic("load configuration",
			zap.Error(err),
			zap.String("config", cfgPath))
	}

	log.Logger.Info("load configuration",
		zap.String("config", cfgPath))
}

// LoadTest loads configuration for tests from an overrideable path.
// It first respects LAISKY_BLOG_TEST_CONFIG and falls back to docs/example config within the repo.
func LoadTest() {
	if cfgPath := os.Getenv(testConfigEnvKey); cfgPath != "" {
		LoadFromFile(cfgPath)
		return
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		log.Logger.Panic("resolve test config path")
	}

	LoadFromFile(filepath.Clean(filepath.Join(filepath.Dir(file), testConfigDefaultPath)))
}
