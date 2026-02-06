package web

import (
	"net"
	"net/http"
	"sort"
	"strings"

	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

const (
	defaultSiteID     = "mcp"
	defaultSiteTitle  = "Laisky MCP"
	defaultSiteRouter = "mcp"
)

// SiteConfig describes the branding and routing settings for a single site.
type SiteConfig struct {
	ID               string   `json:"id"`
	Hosts            []string `json:"hosts,omitempty"`
	Title            string   `json:"title,omitempty"`
	Favicon          string   `json:"favicon,omitempty"`
	Theme            string   `json:"theme,omitempty"`
	Router           string   `json:"router,omitempty"`
	PublicBasePath   string   `json:"publicBasePath,omitempty"`
	TurnstileSiteKey string   `json:"turnstileSiteKey,omitempty"`
	Default          bool     `json:"default,omitempty"`
}

// siteConfigSet stores resolved site configurations and host lookups.
type siteConfigSet struct {
	sites       []SiteConfig
	hostIndex   map[string]SiteConfig
	defaultSite SiteConfig
}

// loadSiteConfigSet loads site configurations from settings and returns a resolved set.
func loadSiteConfigSet(logger logSDK.Logger, prefix urlPrefixConfig) siteConfigSet {
	defaultSite := defaultSiteConfig(prefix)
	rawSites := gconfig.Shared.GetStringMap("settings.web.sites")
	if len(rawSites) == 0 {
		return buildSiteConfigSet([]SiteConfig{defaultSite}, defaultSite)
	}

	keys := make([]string, 0, len(rawSites))
	for key := range rawSites {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	sites := make([]SiteConfig, 0, len(keys))
	for _, key := range keys {
		site := loadSiteConfig(logger, key, prefix)
		if site.ID == "" {
			continue
		}
		sites = append(sites, site)
	}

	if len(sites) == 0 {
		return buildSiteConfigSet([]SiteConfig{defaultSite}, defaultSite)
	}

	defaultSite = chooseDefaultSite(logger, sites, defaultSite)
	return buildSiteConfigSet(sites, defaultSite)
}

// buildSiteConfigSet builds lookup tables for the provided sites and default.
func buildSiteConfigSet(sites []SiteConfig, defaultSite SiteConfig) siteConfigSet {
	hostIndex := make(map[string]SiteConfig)
	for _, site := range sites {
		for _, host := range site.Hosts {
			if host == "" {
				continue
			}
			hostIndex[host] = site
		}
	}

	return siteConfigSet{
		sites:       sites,
		hostIndex:   hostIndex,
		defaultSite: defaultSite,
	}
}

// loadSiteConfig loads a single site configuration from settings.
func loadSiteConfig(logger logSDK.Logger, key string, prefix urlPrefixConfig) SiteConfig {
	baseKey := "settings.web.sites." + key
	hosts := normalizeHostList(gconfig.Shared.GetStringSlice(baseKey + ".hosts"))
	if len(hosts) == 0 {
		host := normalizeHost(gconfig.Shared.GetString(baseKey + ".host"))
		if host != "" {
			hosts = []string{host}
		}
	}

	site := SiteConfig{
		ID:               strings.TrimSpace(key),
		Hosts:            hosts,
		Title:            strings.TrimSpace(gconfig.Shared.GetString(baseKey + ".title")),
		Favicon:          strings.TrimSpace(gconfig.Shared.GetString(baseKey + ".favicon")),
		Theme:            strings.TrimSpace(gconfig.Shared.GetString(baseKey + ".theme")),
		Router:           strings.ToLower(strings.TrimSpace(gconfig.Shared.GetString(baseKey + ".router"))),
		PublicBasePath:   normalizeBasePath(gconfig.Shared.GetString(baseKey + ".public_base_path")),
		TurnstileSiteKey: strings.TrimSpace(gconfig.Shared.GetString(baseKey + ".turnstile_site_key")),
		Default:          gconfig.Shared.GetBool(baseKey + ".default"),
	}

	if site.PublicBasePath == "" {
		site.PublicBasePath = prefix.public
	}
	if site.Router == "" {
		site.Router = defaultSiteRouter
	}
	if site.Title == "" {
		site.Title = defaultSiteTitle
	}

	if site.ID == "" {
		logger.Debug("skip site config without id", zap.String("key", key))
	}

	return site
}

// chooseDefaultSite selects a default site from the candidate list.
func chooseDefaultSite(logger logSDK.Logger, sites []SiteConfig, fallback SiteConfig) SiteConfig {
	for _, site := range sites {
		if site.Default {
			return site
		}
	}

	if len(sites) > 0 {
		return sites[0]
	}

	logger.Debug("no site config available, using fallback")
	return fallback
}

// defaultSiteConfig builds the fallback site configuration.
func defaultSiteConfig(prefix urlPrefixConfig) SiteConfig {
	return SiteConfig{
		ID:             defaultSiteID,
		Title:          defaultSiteTitle,
		Router:         defaultSiteRouter,
		PublicBasePath: prefix.public,
		Default:        true,
	}
}

// resolveForRequest resolves the site configuration that matches the incoming request.
func (s siteConfigSet) resolveForRequest(r *http.Request) SiteConfig {
	host := requestHost(r)
	if host == "" {
		return s.defaultSite
	}

	if site, ok := s.hostIndex[host]; ok {
		return site
	}

	return s.defaultSite
}

// requestHost extracts and normalizes the host for the incoming request.
func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}

	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return normalizeHost(parts[0])
		}
	}

	return normalizeHost(r.Host)
}

// normalizeHostList normalizes hostnames and drops empty values.
func normalizeHostList(hosts []string) []string {
	result := make([]string, 0, len(hosts))
	for _, host := range hosts {
		normalized := normalizeHost(host)
		if normalized == "" {
			continue
		}
		result = append(result, normalized)
	}
	return result
}

// normalizeHost lowercases a hostname and removes port or trailing dot suffixes.
func normalizeHost(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	trimmed = strings.TrimSuffix(trimmed, ".")
	if trimmed == "" {
		return ""
	}

	host, _, err := net.SplitHostPort(trimmed)
	if err == nil {
		return strings.TrimSuffix(strings.ToLower(host), ".")
	}

	if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
		withoutBrackets := strings.TrimPrefix(trimmed, "[")
		withoutBrackets = strings.TrimSuffix(withoutBrackets, "]")
		return strings.TrimSuffix(withoutBrackets, ".")
	}

	return trimmed
}
