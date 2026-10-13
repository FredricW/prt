package surface

import (
	"context"
	"testing"
)

func TestFlags(t *testing.T) {
	runCases(t, []caseTest{
		{"launchdarkly", "added", []string{"added flag: new-checkout", "added flag: dark-mode", "added flag: billing-v2"}},
		{"launchdarkly", "removed", []string{"removed flag: old-banner"}},
		{"launchdarkly", "changed", []string{"changed flag: checkout-v2"}},
		{"launchdarkly", "negative", nil},

		{"unleash", "added", []string{"added flag: new-search", "added flag: beta-banner", "added flag: go-search"}},
		{"unleash", "removed", []string{"removed flag: pricing-test"}},
		{"unleash", "changed", []string{"changed flag: search-v2"}},
		{"unleash", "negative", nil},

		{"growthbook", "added", []string{"added flag: new-nav", "added flag: button-color", "added flag: promo"}},
		{"growthbook", "removed", []string{"removed flag: upload-limit"}},
		{"growthbook", "changed", []string{"changed flag: nav-v2"}},
		{"growthbook", "negative", nil},

		{"openfeature", "added", []string{"added flag: new-flow", "added flag: go-flow", "added flag: py-flow"}},
		{"openfeature", "removed", []string{"removed flag: theme"}},
		{"openfeature", "changed", []string{"changed flag: flow-b"}},
		{"openfeature", "negative", nil},

		{"custom", "added", []string{"added flag: user-preferences-v2", "added flag: new-ui", "added flag: beta",
			"added flag: compactMode", "added flag: py-flag"}},
		{"custom", "removed", []string{"removed flag: legacy-export"}},
		{"custom", "changed", []string{"changed flag: user-preferences-v2"}},
		{"custom", "negative", nil},

		{"env", "added", []string{"added env: PORTAL_OPS_ADDR", "added env: FEATURE_X", "added env: PORT", "added env: API_KEY",
			"added env: TIMEOUT_MS", "added env: VITE_API_URL", "added env: PORTAL_TOKEN", "added env: PORTAL_API",
			"added env: DEBUG", "added env: SECRET_KEY", "added env: NEW_VAR"}},
		{"env", "removed", []string{"removed env: PORTAL_LEGACY_MODE"}},
		{"env", "changed", []string{"changed env: NEW_NAME"}},
		{"env", "negative", nil},

		{"config", "added", []string{"added config key: server.addr", "added config key: -port", "added config key: -mode",
			"added config key: --config", "added config key: db.pool", "added config key: settings.FEATURE_X",
			"added config key: --dry-run", "added config key: settings.NEW_SETTING"}},
		{"config", "removed", []string{"removed config key: workers"}},
		{"config", "changed", []string{"changed config key: server.addr"}},
		{"config", "negative", nil},
	})
}

// TestFlagDetails checks frameworks, rename details and confidences.
func TestFlagDetails(t *testing.T) {
	tests := []struct {
		dir, name, words, fw, detail string
		minConf                      float64
	}{
		{"launchdarkly", "added", "added flag: billing-v2", fwLaunchDarkly, "", 0.95},
		{"unleash", "added", "added flag: new-search", fwUnleash, "", 0.95},
		{"growthbook", "added", "added flag: promo", fwGrowthBook, "", 0.95},
		{"openfeature", "added", "added flag: go-flow", fwOpenFeature, "", 0.95},
		{"custom", "added", "added flag: user-preferences-v2", fwCustom, "", 0.95},
		{"custom", "added", "added flag: compactMode", fwCustom, "", 0.75},
		{"custom", "changed", "changed flag: user-preferences-v2", fwCustom, "was user-preferences", 0.95},
		{"env", "added", "added env: PORTAL_OPS_ADDR", fwEnv, "", 0.95},
		{"env", "added", "added env: NEW_VAR", fwDotenv, "", 0.75},
		{"env", "changed", "changed env: NEW_NAME", fwEnv, "was OLD_NAME", 0.95},
		{"config", "added", "added config key: server.addr", fwViper, "", 0.95},
		{"config", "added", "added config key: settings.FEATURE_X", fwDjango, "", 0.9},
	}
	for _, tc := range tests {
		t.Run(tc.dir+"/"+tc.words, func(t *testing.T) {
			cs := build(context.Background(), patchInput(t, loadCases(t, tc.dir)[tc.name]))
			e := find(t, cs, tc.words)
			if e.Framework != tc.fw || e.Detail != tc.detail || e.Confidence < tc.minConf {
				t.Errorf("entry = %+v, want framework %s, detail %q, confidence >= %.2f", e, tc.fw, tc.detail, tc.minConf)
			}
		})
	}
}
