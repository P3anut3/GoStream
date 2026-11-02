package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRuntimeRejectsInvalidConfiguration(t *testing.T) {
	const validYAML = "http:\n  addr: 127.0.0.1:8080\nstorage:\n  video_dir: media/videos\n  cover_dir: media/covers\n"
	for _, tc := range []struct {
		name, body, key, value, dotenv, want string
	}{
		{name: "missing_yaml", want: "读取配置文件失败"},
		{name: "invalid_yaml", body: "unknown: private-marker\n", want: "YAML 配置格式错误"},
		{name: "empty_directory_override", body: validYAML, key: "GOSTREAM_VIDEO_DIR", want: "视频保存目录不能为空"},
		{name: "invalid_port_override", body: validYAML, key: "GOSTREAM_HTTP_ADDR", value: "127.0.0.1:65536", want: "HTTP 端口"},
		{name: "missing_dsn", body: validYAML, key: "GOSTREAM_DSN", want: "GOSTREAM_DSN"},
		{name: "short_jwt_secret", body: validYAML, key: "JWT_SECRET", value: "private-marker", want: "JWT_SECRET"},
		{name: "short_cursor_secret", body: validYAML, key: "GOSTREAM_FEED_CURSOR_SECRET", value: "private-marker", want: "GOSTREAM_FEED_CURSOR_SECRET"},
		{name: "malformed_dotenv", body: validYAML, dotenv: "JWT_SECRET=\"unterminated-private-marker\n", want: "加载 .env 失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("GOSTREAM_DSN", "test-user:private-marker@tcp(127.0.0.1:1)/unused")
			t.Setenv("JWT_SECRET", strings.Repeat("j", 32))
			t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", strings.Repeat("f", 32))
			t.Setenv("GOSTREAM_HTTP_ADDR", "127.0.0.1:8080")
			t.Setenv("GOSTREAM_VIDEO_DIR", "media/videos")
			t.Setenv("GOSTREAM_COVER_DIR", "media/covers")
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			if tc.body != "" {
				dir := filepath.Join(root, "internal", "config")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.dotenv != "" {
				if err := os.WriteFile(filepath.Join(root, ".env"), []byte(tc.dotenv), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runtime, err := openRuntime()
			if err == nil {
				runtime.close()
				t.Fatal("invalid configuration must be rejected")
			}
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("configuration error leaked a value")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected configuration error %s, got %v", tc.want, err)
			}
			if runtime.repository != nil || runtime.runner != nil || runtime.close != nil {
				t.Fatal("invalid configuration must not expose a partial runtime")
			}
		})
	}
}
