package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// TestLoad_AnalyticsCommunityTimeout_Default exercises the lib-7gyz default:
// when no user config sets analytics.community_timeout, Load() must return
// the 60s production default (NOT 0, which would mean "disabled" / legacy
// unbounded Louvain — exactly the bug Phase 2 is fixing).
func TestLoad_AnalyticsCommunityTimeout_Default(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 60*time.Second {
		t.Errorf("default CommunityTimeout = %s, want 60s", cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_AnalyticsCommunityTimeout_UserOverride verifies the user can
// shorten the bound via config. mapstructure decodes Go duration strings
// natively (e.g. "5s" → 5 * time.Second).
func TestLoad_AnalyticsCommunityTimeout_UserOverride(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("analytics:\n  community_timeout: 5s\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 5*time.Second {
		t.Errorf("user-overridden CommunityTimeout = %s, want 5s", cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_AnalyticsCommunityTimeout_ExplicitDisable verifies the explicit
// "0" sentinel for disabling the bound survives Load(). This is the escape
// hatch for users who genuinely want to wait indefinitely; the IsSet guard
// must NOT clobber it back to the 60s default.
func TestLoad_AnalyticsCommunityTimeout_ExplicitDisable(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("analytics:\n  community_timeout: 0\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 0 {
		t.Errorf("explicit 0 should disable the bound; got %s", cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_AnalyticsCommunityTimeout_PartialAnalyticsBlock guards the IsSet
// edge case: a user has an `analytics:` section in their config with
// future-keys-we-don't-have-yet but does NOT set community_timeout. Without
// the IsSet guard, mapstructure would Unmarshal the missing duration as 0
// and silently disable the bound. With the guard, the 60s default survives.
func TestLoad_AnalyticsCommunityTimeout_PartialAnalyticsBlock(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	// Hypothetical future key under analytics: that doesn't shadow our default.
	if err := viper.ReadConfig(strings.NewReader("analytics:\n  some_future_key: foo\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 60*time.Second {
		t.Errorf("partial analytics block clobbered default: got %s, want 60s",
			cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_GraphEnabled_Default verifies graph.enabled defaults to true so the
// graph pass keeps running for users who never set the key.
func TestLoad_GraphEnabled_Default(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cfg := Load()
	if !cfg.Graph.Enabled {
		t.Errorf("default Graph.Enabled = false, want true")
	}
}

// TestLoad_GraphEnabled_ExplicitDisable verifies a docs-only workspace can
// turn the graph pass off persistently. The IsSet guard must NOT clobber an
// explicit `false` back to the true default.
func TestLoad_GraphEnabled_ExplicitDisable(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("graph:\n  enabled: false\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Graph.Enabled {
		t.Errorf("explicit graph.enabled: false should disable the graph pass; got true")
	}
}

// TestLoad_GraphEnabled_PartialGraphBlock guards the IsSet edge case: a user
// has a `graph:` section that sets some other key but omits `enabled`. Without
// the guard, mapstructure would decode the missing bool as false and silently
// disable the graph pass; with it, the true default survives.
func TestLoad_GraphEnabled_PartialGraphBlock(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("graph:\n  honor_gitignore: false\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if !cfg.Graph.Enabled {
		t.Errorf("partial graph block clobbered default: Graph.Enabled = false, want true")
	}
}
