package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// WorkerConfig 是 cmd/worker 的配置：要数据库和 RabbitMQ，Redis 可选，不需要 JWT 之类 API 才用的密钥。
type WorkerConfig struct {
	DSN     string // GOSTREAM_DSN，和 API 相同
	AMQPURL string // GOSTREAM_AMQP_URL，形如 amqp://用户名:密码@主机:5672/
	// Redis 和 API 用同一组变量 GOSTREAM_REDIS_ADDR、GOSTREAM_REDIS_PASSWORD；不配置时 worker 照常写通知，
	// 只是不发"有新通知"的提示，页面要等下一次轮询才看到。
	Redis RedisConfig
}

// LoadWorker 从环境变量读取并检查 worker 配置。
func LoadWorker() (WorkerConfig, error) {
	cfg := WorkerConfig{
		DSN:     os.Getenv("GOSTREAM_DSN"),
		AMQPURL: os.Getenv("GOSTREAM_AMQP_URL"),
		Redis:   RedisConfig{Addr: strings.TrimSpace(os.Getenv("GOSTREAM_REDIS_ADDR")), Password: os.Getenv("GOSTREAM_REDIS_PASSWORD")},
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return WorkerConfig{}, errors.New("请配置 GOSTREAM_DSN")
	}
	if strings.TrimSpace(cfg.AMQPURL) == "" {
		return WorkerConfig{}, errors.New("请配置 GOSTREAM_AMQP_URL")
	}
	// 地址里有密码：格式不对时只说哪个变量有问题，不把地址本身放进错误信息。
	u, err := url.Parse(cfg.AMQPURL)
	if err != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.Hostname() == "" {
		return WorkerConfig{}, errors.New("GOSTREAM_AMQP_URL 格式不正确，应为 amqp://用户名:密码@主机:端口/（密码里的特殊字符要做 URL 编码）")
	}
	return cfg, nil
}
