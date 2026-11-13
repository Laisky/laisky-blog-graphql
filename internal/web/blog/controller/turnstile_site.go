package controller

import (
	"sort"
	"strings"

	gconfig "github.com/Laisky/go-config/v2"
)

// turnstileGlobalSecretKey and turnstileGlobalSiteKey name the settings of
// the pair used when no site supplies one.
const (
	turnstileGlobalSecretKey = "settings.web.turnstile.secret_key" //nolint:gosec // G101: a configuration key name, not a credential
	turnstileGlobalSiteKey   = "settings.web.turnstile.site_key"
)

// turnstileSite is the Turnstile widget site key and the matching secret that
// serve one request host. The pair always comes from one place, so a token
// produced by the widget the page renders verifies against the secret the
// server checks it with.
type turnstileSite struct {
	// siteKey is the public widget key, or empty when the pair has none, in
	// which case the page on this host cannot render a challenge.
	siteKey string
	// secret is the siteverify secret; empty only when Turnstile is not
	// configured anywhere.
	secret string
}

// turnstileSiteEntry is one configured site that carries a Turnstile secret.
type turnstileSiteEntry struct {
	id        string
	base      string
	isDefault bool
	site      turnstileSite
}

// resolveTurnstileSite selects the Turnstile pair for requestHost. The request
// host comes from the client (Host, or X-Forwarded-Host from a trusted proxy),
// so it may choose which configured pair applies but never whether one does:
// once any secret is configured the result always carries a secret. The order
// is:
//
//  1. the site whose hosts include requestHost, if it has a secret;
//  2. the site marked default, if it has a secret;
//  3. the global settings.web.turnstile pair;
//  4. the only site with a secret;
//  5. several sites with secrets but no default and no global secret is
//     rejected at startup (see cmd validation); should it happen anyway, the
//     first such site by key, so the gate stays on.
func resolveTurnstileSite(requestHost string) turnstileSite {
	global := turnstileSite{
		siteKey: strings.TrimSpace(gconfig.Shared.GetString(turnstileGlobalSiteKey)),
		secret:  strings.TrimSpace(gconfig.Shared.GetString(turnstileGlobalSecretKey)),
	}
	entries := loadTurnstileSiteEntries()

	if requestHost != "" {
		for _, entry := range entries {
			if isTurnstileHostMatched(entry.base, requestHost) {
				return entry.site
			}
		}
	}
	for _, entry := range entries {
		if entry.isDefault {
			return entry.site
		}
	}
	if global.secret != "" {
		return global
	}
	if len(entries) > 0 {
		return entries[0].site
	}
	return turnstileSite{}
}

// loadTurnstileSiteEntries returns the configured sites that carry a Turnstile
// secret, ordered by site key.
func loadTurnstileSiteEntries() []turnstileSiteEntry {
	rawSites := gconfig.Shared.GetStringMap("settings.web.sites")
	keys := make([]string, 0, len(rawSites))
	for key := range rawSites {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]turnstileSiteEntry, 0, len(keys))
	for _, key := range keys {
		base := "settings.web.sites." + key
		secret := strings.TrimSpace(gconfig.Shared.GetString(base + ".turnstile_secret_key"))
		if secret == "" {
			continue
		}
		entries = append(entries, turnstileSiteEntry{
			id:        key,
			base:      base,
			isDefault: gconfig.Shared.GetBool(base + ".default"),
			site: turnstileSite{
				siteKey: strings.TrimSpace(gconfig.Shared.GetString(base + ".turnstile_site_key")),
				secret:  secret,
			},
		})
	}
	return entries
}

// TurnstileSiteKeyForHost returns the widget site key the SSO page served on
// requestHost must render so its tokens verify against the secret the server
// selects for that host, or "" when Turnstile is not configured or the
// selected pair has no site key.
func TurnstileSiteKeyForHost(requestHost string) string {
	site := resolveTurnstileSite(normalizeTurnstileHost(requestHost))
	if site.secret == "" {
		return ""
	}
	return site.siteKey
}
