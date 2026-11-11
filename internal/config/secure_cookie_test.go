package config

import "testing"

func TestSecureCookieComesFromEnvironment(t *testing.T) {
	for _, value := range []string{"", "true", "false"} {
		t.Setenv("GOSTREAM_SECURE_COOKIE", value)
		var cfg Config
		applyEnv(&cfg)
		if cfg.Auth.SecureCookie != value {
			t.Fatalf("GOSTREAM_SECURE_COOKIE=%q must reach the config, got %q", value, cfg.Auth.SecureCookie)
		}
	}
}
