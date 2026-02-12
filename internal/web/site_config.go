package web

import (
	"net"
	"net/http"
	"net/url"
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
	pathIndex   []sitePathMatch
	defaultSite SiteConfig
}

// sitePathMatch stores a base path and the site it maps to.
type sitePathMatch struct {
	base string
	site SiteConfig
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

	pathIndex := buildPathIndex(sites)

	return siteConfigSet{
		sites:       sites,
		hostIndex:   hostIndex,
		pathIndex:   pathIndex,
		defaultSite: defaultSite,
	}
}

// buildPathIndex builds the ordered list of base-path matches for sites.
func buildPathIndex(sites []SiteConfig) []sitePathMatch {
	matches := make([]sitePathMatch, 0, len(sites))
	seen := make(map[string]struct{})
	for _, site := range sites {
		base := normalizeBasePath(site.PublicBasePath)
		if base == "" {
			continue
		}
		if _, ok := seen[base]; ok {
			continue
		}
		seen[base] = struct{}{}
		matches = append(matches, sitePathMatch{base: base, site: site})
	}

	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].base) == len(matches[j].base) {
			return matches[i].base < matches[j].base
		}
		return len(matches[i].base) > len(matches[j].base)
	})

	return matches
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

	if site, ok := s.matchByPath(r.URL.Path); ok {
		return site
	}

	if site, ok := s.matchByPath(requestPathFromReferer(r)); ok {
		return site
	}

	return s.defaultSite
}

// matchByPath tries to resolve a site by matching path prefixes.
func (s siteConfigSet) matchByPath(path string) (SiteConfig, bool) {
	if path == "" || !strings.HasPrefix(path, "/") {
		return SiteConfig{}, false
	}

	for _, match := range s.pathIndex {
		if path == match.base || strings.HasPrefix(path, match.base+"/") {
			return match.site, true
		}
	}

	return SiteConfig{}, false
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

// requestPathFromReferer extracts the path from the Referer header when available.
func requestPathFromReferer(r *http.Request) string {
	if r == nil {
		return ""
	}

	ref := strings.TrimSpace(r.Referer())
	if ref == "" {
		return ""
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return ""
	}

	return parsed.Path
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
