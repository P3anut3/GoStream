package config

import (
	"strings"
	"testing"
)

func validConfigFixture() Config {
	return Config{
		HTTP:     HTTPConfig{Addr: "127.0.0.1:8080"},
		Storage:  StorageConfig{VideoDir: "test-media/videos", CoverDir: "test-media/covers"},
		Database: DatabaseConfig{DSN: "test-user:config-private-marker@tcp(127.0.0.1:13306)/config_test"},
		Auth:     AuthConfig{JWTSecret: strings.Repeat("j", 32), FeedCursorSecret: strings.Repeat("f", 32)},
	}
}

func TestValidateConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{"合法配置", func(c *Config) {}, ""},
		{"容器监听地址", func(c *Config) { c.HTTP.Addr = ":8080" }, ""},
		{"IPv6监听地址", func(c *Config) { c.HTTP.Addr = "[::1]:8080" }, ""},
		{"最小端口", func(c *Config) { c.HTTP.Addr = "127.0.0.1:1" }, ""},
		{"最大端口", func(c *Config) { c.HTTP.Addr = "127.0.0.1:65535" }, ""},
		{"空监听地址", func(c *Config) { c.HTTP.Addr = " \t" }, "监听地址不能为空"},
		{"缺少端口", func(c *Config) { c.HTTP.Addr = "localhost" }, "host:port"},
		{"非数字端口", func(c *Config) { c.HTTP.Addr = "localhost:http" }, "端口"},
		{"零端口", func(c *Config) { c.HTTP.Addr = "localhost:0" }, "端口"},
		{"负数端口", func(c *Config) { c.HTTP.Addr = "localhost:-1" }, "端口"},
		{"端口超限", func(c *Config) { c.HTTP.Addr = "localhost:65536" }, "端口"},
		{"空视频目录", func(c *Config) { c.Storage.VideoDir = " \t" }, "视频保存目录"},
		{"空封面目录", func(c *Config) { c.Storage.CoverDir = " \t" }, "封面保存目录"},
		{"空数据库配置", func(c *Config) { c.Database.DSN = " \t" }, "GOSTREAM_DSN"},
		{"短JWT密钥", func(c *Config) { c.Auth.JWTSecret = strings.Repeat("j", 31) }, "JWT_SECRET"},
		{"空白JWT密钥", func(c *Config) { c.Auth.JWTSecret = strings.Repeat(" ", 32) }, "JWT_SECRET"},
		{"短游标签名密钥", func(c *Config) { c.Auth.FeedCursorSecret = "private-marker" }, "GOSTREAM_FEED_CURSOR_SECRET"},
		{"空白游标签名密钥", func(c *Config) { c.Auth.FeedCursorSecret = strings.Repeat(" ", 32) }, "GOSTREAM_FEED_CURSOR_SECRET"},
		{"HTTPS部署打开安全Cookie", func(c *Config) { c.Auth.SecureCookie = "true" }, ""},
		{"本地开发关闭安全Cookie", func(c *Config) { c.Auth.SecureCookie = "false" }, ""},
		{"大写TRUE不被接受", func(c *Config) { c.Auth.SecureCookie = "TRUE" }, "GOSTREAM_SECURE_COOKIE"},
		{"数字1不被接受", func(c *Config) { c.Auth.SecureCookie = "1" }, "GOSTREAM_SECURE_COOKIE"},
		{"未配置Redis表示不启用缓存", func(c *Config) { c.Redis = RedisConfig{} }, ""},
		{"容器内Redis地址", func(c *Config) { c.Redis = RedisConfig{Addr: "redis:6379", Password: "private-marker"} }, ""},
		{"Redis缺少端口", func(c *Config) { c.Redis.Addr = "private-marker" }, "Redis 地址"},
		{"Redis端口超限", func(c *Config) { c.Redis.Addr = "redis:65536" }, "Redis 端口"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigFixture()
			tc.change(&cfg)
			before := cfg
			err := cfg.Validate()
			if cfg != before {
				t.Fatal("校验不应修改配置或密钥")
			}
			if tc.want == "" {
				if err != nil {
					t.Fatalf("合法配置不应被拒绝: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("无效配置应被拒绝")
			}
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("校验错误不得包含实际配置值")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误应指明配置项 %s，实际: %v", tc.want, err)
			}
		})
	}
}

func TestLoadValidatesFinalEnvironmentValues(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"空地址覆盖有效YAML", "GOSTREAM_HTTP_ADDR", ""},
		{"无效端口覆盖有效YAML", "GOSTREAM_HTTP_ADDR", "127.0.0.1:65536"},
		{"空视频目录覆盖有效YAML", "GOSTREAM_VIDEO_DIR", ""},
		{"空封面目录覆盖有效YAML", "GOSTREAM_COVER_DIR", ""},
		{"缺少DSN", "GOSTREAM_DSN", ""},
		{"短JWT密钥", "JWT_SECRET", "private-marker"},
		{"短游标签名密钥", "GOSTREAM_FEED_CURSOR_SECRET", "private-marker"},
		{"无效Redis地址", "GOSTREAM_REDIS_ADDR", "private-marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigFixture()
			t.Setenv("GOSTREAM_HTTP_ADDR", cfg.HTTP.Addr)
			t.Setenv("GOSTREAM_VIDEO_DIR", cfg.Storage.VideoDir)
			t.Setenv("GOSTREAM_COVER_DIR", cfg.Storage.CoverDir)
			t.Setenv("GOSTREAM_DSN", cfg.Database.DSN)
			t.Setenv("JWT_SECRET", cfg.Auth.JWTSecret)
			t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", cfg.Auth.FeedCursorSecret)
			t.Setenv(tc.key, tc.value)
			got, err := Load(writeConfigFixture(t, `http:
  addr: "127.0.0.1:8080"
storage:
  video_dir: "original/videos"
  cover_dir: "original/covers"
`))
			if err == nil {
				t.Fatal("覆盖后的无效配置应被 Load 拒绝")
			}
			if got != (Config{}) {
				t.Fatal("Load 不应返回部分有效配置")
			}
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("Load 错误不得泄漏配置值")
			}
		})
	}
}
