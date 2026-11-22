package workbench

import (
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

var accountLanguagePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// These are product defaults for common regions, not a claim that a country
// has one language. Unlisted regions retain the user's applied language.
var regionLanguage = map[string]string{
	"US": "en", "GB": "en", "CA": "en", "AU": "en", "NZ": "en", "IE": "en", "SG": "en", "IN": "en", "PH": "en", "ZA": "en",
	"JP": "ja", "KR": "ko", "CN": "zh", "TW": "zh", "HK": "zh",
	"DE": "de", "AT": "de", "CH": "de", "FR": "fr", "BE": "nl", "NL": "nl", "SE": "sv", "FI": "fi", "NO": "nb", "DK": "da",
	"ES": "es", "MX": "es", "AR": "es", "CL": "es", "PT": "pt", "BR": "pt", "IT": "it", "PL": "pl", "CZ": "cs", "RO": "ro", "HU": "hu", "GR": "el",
	"ID": "id", "MY": "ms", "VN": "vi", "IL": "he", "TR": "tr", "UA": "uk", "RU": "ru", "AE": "ar", "SA": "ar",
}

func languageSuggestion(country string) (locale, browser string) {
	if language := regionLanguage[country]; language != "" {
		return language + "_" + country + ".UTF-8", language + "-" + country
	}
	return "", ""
}

func autoRegionSettings(cfg cage.Config, preferences Preferences, location Location) cage.Config {
	if preferences.AutoTimezone && location.Timezone != "" {
		cfg.Timezone = location.Timezone
	}
	if preferences.AutoLanguage {
		if locale, browser := languageSuggestion(location.CountryCode); locale != "" {
			cfg.Locale, cfg.BrowserLanguage = locale, browser
		}
	}
	return cfg
}

func accountRegionOverrides(profile, nodeID string, preferences Preferences) (tz, language string) {
	if nodeID == "" || (!preferences.AutoTimezone && !preferences.AutoLanguage) {
		return "", ""
	}
	var country, timezone string
	if loc, ok := cage.LoadNodeLocation(profile, nodeID); ok {
		country, timezone = loc.CountryCode, loc.Timezone
	}
	if country == "" {
		if store, err := managedproxy.Load(profile); err == nil {
			if policy, ok := store.ExitPolicies[nodeID]; ok {
				country = strings.ToUpper(strings.TrimSpace(policy.CountryCode))
			}
		}
	}
	if preferences.AutoTimezone && timezone != "" && timezone != "Local" {
		if _, err := time.LoadLocation(timezone); err == nil {
			tz = timezone
		}
	}
	if preferences.AutoLanguage && country != "" {
		if _, browser := languageSuggestion(country); accountLanguagePattern.MatchString(browser) {
			language = browser
		}
	}
	return tz, language
}

func (s *Server) accountRegionOverrides(nodeID string) (tz, language string) {
	s.mu.Lock()
	prefs := s.preferences
	s.mu.Unlock()
	return accountRegionOverrides(s.profile, nodeID, prefs)
}
