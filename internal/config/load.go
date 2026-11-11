package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

func applyEnv(cfg *Config) {
	if value, exists := os.LookupEnv("GOSTREAM_HTTP_ADDR"); exists {
		cfg.HTTP.Addr = value
	}

	if value, exists := os.LookupEnv("GOSTREAM_VIDEO_DIR"); exists {
		cfg.Storage.VideoDir = value
	}

	if value, exists := os.LookupEnv("GOSTREAM_COVER_DIR"); exists {
		cfg.Storage.CoverDir = value
	}

	if value, exists := os.LookupEnv("GOSTREAM_REDIS_ADDR"); exists {
		cfg.Redis.Addr = value
	}

	cfg.Redis.Password = os.Getenv("GOSTREAM_REDIS_PASSWORD")
	cfg.Database.DSN = os.Getenv("GOSTREAM_DSN")
	cfg.Auth.JWTSecret = os.Getenv("JWT_SECRET")
	cfg.Auth.FeedCursorSecret = os.Getenv("GOSTREAM_FEED_CURSOR_SECRET")
	cfg.Auth.SecureCookie = os.Getenv("GOSTREAM_SECURE_COOKIE")
}

// 读取 YAML → 环境变量覆盖 → 校验最终配置 → 返回 Config
func Load(path string) (Config, error) {
	var cfg Config

	// 1. 读取文件，得到文件内容的字节切片。
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 2. 解析 YAML，把数据填入 cfg。
	err = yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict())
	if err != nil {
		return Config{}, errors.New("YAML 配置格式错误或存在未知字段")
	}

	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	// 3. 返回填好数据的配置对象。
	return cfg, nil
}
