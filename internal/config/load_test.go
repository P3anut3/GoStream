package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadYAML(t *testing.T) {
	// Explicit values make this independent of the developer's process environment.
	// They also keep the YAML assertions valid once environment loading is added.
	t.Setenv("GOSTREAM_HTTP_ADDR", "127.0.0.1:19080")
	t.Setenv("GOSTREAM_VIDEO_DIR", "test-media/videos")
	t.Setenv("GOSTREAM_COVER_DIR", "test-media/covers")
	t.Setenv("GOSTREAM_DSN", "test-user:test-password@tcp(127.0.0.1:13306)/config_test")
	t.Setenv("JWT_SECRET", strings.Repeat("j", 32))
	t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", strings.Repeat("f", 32))
	path := writeConfigFixture(t, `http:
  addr: "127.0.0.1:19080"
storage:
  video_dir: "test-media/videos"
  cover_dir: "test-media/covers"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载合法 YAML 失败: %v", err)
	}
	if cfg.HTTP.Addr != "127.0.0.1:19080" || cfg.Storage.VideoDir != "test-media/videos" || cfg.Storage.CoverDir != "test-media/covers" {
		t.Fatal("YAML 字段未正确映射至 HTTP/Storage 配置")
	}
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"未知顶层字段", "unexpected: config-test-private-marker\n"},
		{"拼错监听地址字段", "http:\n  adrr: config-test-private-marker\n"},
		{"语法错误", "http: [config-test-private-marker\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfigFixture(t, tc.body))
			if err == nil {
				t.Fatal("无效 YAML 应被拒绝")
			}
			if cfg != (Config{}) {
				t.Fatal("加载失败不应返回部分配置")
			}
			if strings.Contains(err.Error(), "config-test-private-marker") {
				t.Fatal("错误信息不应回显 YAML 内容")
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("应保留文件不存在的错误原因，实际: %v", err)
	}
	if cfg != (Config{}) {
		t.Fatal("文件读取失败不应返回可用配置")
	}
}

func TestLoadEnvironmentOverridesYAML(t *testing.T) {
	t.Setenv("GOSTREAM_HTTP_ADDR", "127.0.0.1:29080")
	t.Setenv("GOSTREAM_VIDEO_DIR", "override/videos")
	t.Setenv("GOSTREAM_COVER_DIR", "override/covers")
	dsn := "test-user:test-password@tcp(127.0.0.1:13306)/config_test"
	jwtSecret, cursorSecret := strings.Repeat("j", 32), strings.Repeat("f", 32)
	t.Setenv("GOSTREAM_DSN", dsn)
	t.Setenv("JWT_SECRET", jwtSecret)
	t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", cursorSecret)
	cfg, err := Load(writeConfigFixture(t, `http:
  addr: "127.0.0.1:8080"
storage:
  video_dir: "original/videos"
  cover_dir: "original/covers"
`))
	if err != nil {
		t.Fatalf("加载带环境覆盖的配置失败: %v", err)
	}
	if cfg.HTTP.Addr != "127.0.0.1:29080" || cfg.Storage.VideoDir != "override/videos" || cfg.Storage.CoverDir != "override/covers" {
		t.Fatal("环境变量应覆盖 YAML 中的非敏感配置")
	}
	if cfg.Database.DSN != dsn || cfg.Auth.JWTSecret != jwtSecret || cfg.Auth.FeedCursorSecret != cursorSecret {
		t.Fatal("数据库和认证配置应从环境变量加载")
	}
}

func TestLoadRedisIsOptionalAndPasswordComesFromEnvironment(t *testing.T) {
	t.Setenv("GOSTREAM_DSN", "test-user:test-password@tcp(127.0.0.1:13306)/config_test")
	t.Setenv("JWT_SECRET", strings.Repeat("j", 32))
	t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", strings.Repeat("f", 32))
	for _, key := range []string{"GOSTREAM_HTTP_ADDR", "GOSTREAM_VIDEO_DIR", "GOSTREAM_COVER_DIR", "GOSTREAM_REDIS_ADDR", "GOSTREAM_REDIS_PASSWORD"} {
		t.Setenv(key, "temporary")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	base := "http:\n  addr: \"127.0.0.1:8080\"\nstorage:\n  video_dir: \"v\"\n  cover_dir: \"c\"\n"
	// Without a redis section or environment the cache stays disabled.
	cfg, err := Load(writeConfigFixture(t, base))
	if err != nil || cfg.Redis != (RedisConfig{}) {
		t.Fatalf("Redis must be optional and disabled by default: %+v, %v", cfg.Redis, err)
	}
	// The YAML address is used, and the environment enables or overrides it.
	cfg, err = Load(writeConfigFixture(t, base+"redis:\n  addr: \"127.0.0.1:16379\"\n"))
	if err != nil || cfg.Redis.Addr != "127.0.0.1:16379" {
		t.Fatalf("YAML Redis address not loaded: %+v, %v", cfg.Redis, err)
	}
	t.Setenv("GOSTREAM_REDIS_ADDR", "redis:6379")
	t.Setenv("GOSTREAM_REDIS_PASSWORD", "redis-test-password")
	cfg, err = Load(writeConfigFixture(t, base+"redis:\n  addr: \"\"\n"))
	if err != nil || cfg.Redis.Addr != "redis:6379" || cfg.Redis.Password != "redis-test-password" {
		t.Fatalf("environment must enable Redis and supply its password: %+v, %v", cfg.Redis, err)
	}
	// A password written into YAML is rejected instead of silently used.
	cfg, err = Load(writeConfigFixture(t, base+"redis:\n  addr: \"redis:6379\"\n  password: config-test-private-marker\n"))
	if err == nil || cfg != (Config{}) || strings.Contains(err.Error(), "config-test-private-marker") {
		t.Fatalf("a YAML Redis password must be rejected without echoing it: %v", err)
	}
}

func TestApplyEnvUnsetAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name         string
		emptyAddress bool
		wantAddress  string
	}{
		{"未设置时保留原配置", false, "127.0.0.1:8080"},
		{"显式空值也覆盖原配置", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"GOSTREAM_HTTP_ADDR", "GOSTREAM_VIDEO_DIR", "GOSTREAM_COVER_DIR"} {
				// Setenv registers restoration of the original process environment.
				t.Setenv(key, "temporary")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			if tc.emptyAddress {
				t.Setenv("GOSTREAM_HTTP_ADDR", "")
			}
			t.Setenv("GOSTREAM_DSN", "")
			t.Setenv("JWT_SECRET", "")
			t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", "")
			cfg := Config{
				HTTP:    HTTPConfig{Addr: "127.0.0.1:8080"},
				Storage: StorageConfig{VideoDir: "original/videos", CoverDir: "original/covers"},
			}
			applyEnv(&cfg)
			if cfg.HTTP.Addr != tc.wantAddress || cfg.Storage.VideoDir != "original/videos" || cfg.Storage.CoverDir != "original/covers" {
				t.Fatal("未设置与显式空值的覆盖规则不符合约定")
			}
		})
	}
}
