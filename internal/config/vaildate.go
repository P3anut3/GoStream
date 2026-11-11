package config

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.HTTP.Addr) == "" {
		return errors.New("HTTP 监听地址不能为空")
	}

	_, port, err := net.SplitHostPort(cfg.HTTP.Addr)
	if err != nil {
		return errors.New("HTTP 监听地址必须使用 host:port 格式")
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("HTTP 端口必须为 1～65535")
	}

	if strings.TrimSpace(cfg.Storage.VideoDir) == "" {
		return errors.New("视频保存目录不能为空")
	}

	if strings.TrimSpace(cfg.Storage.CoverDir) == "" {
		return errors.New("封面保存目录不能为空")
	}

	// Redis 可以不配置；一旦配置了地址，就必须是可连接的 host:port。
	if strings.TrimSpace(cfg.Redis.Addr) != "" {
		_, redisPort, err := net.SplitHostPort(cfg.Redis.Addr)
		if err != nil {
			return errors.New("Redis 地址必须使用 host:port 格式")
		}
		redisPortNumber, err := strconv.Atoi(redisPort)
		if err != nil || redisPortNumber < 1 || redisPortNumber > 65535 {
			return errors.New("Redis 端口必须为 1～65535")
		}
	}

	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errors.New("请配置 GOSTREAM_DSN")
	}

	if len(cfg.Auth.JWTSecret) < 32 ||
		strings.TrimSpace(cfg.Auth.JWTSecret) == "" {
		return errors.New("JWT_SECRET 至少需要 32 字节且不能全为空白")
	}

	if len(cfg.Auth.FeedCursorSecret) < 32 ||
		strings.TrimSpace(cfg.Auth.FeedCursorSecret) == "" {
		return errors.New("GOSTREAM_FEED_CURSOR_SECRET 至少需要 32 字节且不能全为空白")
	}

	// 只接受明确的写法：写成 TRUE、1 之类时宁可启动失败，也不能悄悄关掉 HTTPS 保护。
	if cfg.Auth.SecureCookie != "" && cfg.Auth.SecureCookie != "true" && cfg.Auth.SecureCookie != "false" {
		return errors.New("GOSTREAM_SECURE_COOKIE 只能是 true 或 false")
	}

	return nil
}
