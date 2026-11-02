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

	return nil
}
