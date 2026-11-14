package config

import (
	"strings"
	"testing"
)

func TestLoadWorker(t *testing.T) {
	const dsn = "user:pass@tcp(127.0.0.1:3306)/gostream?parseTime=true"
	for _, tc := range []struct {
		name, dsn, url, wantErr  string
		redisAddr, redisPassword string
		wantRedis                RedisConfig
	}{
		{name: "valid", dsn: dsn, url: "amqp://gostream:s3cret@rabbitmq:5672/"},
		{name: "tls", dsn: dsn, url: "amqps://gostream:s3cret@mq.example.com:5671/"},
		// Redis is optional: with it the worker also sends new-notification signals.
		{name: "with_redis", dsn: dsn, url: "amqp://gostream:s3cret@rabbitmq:5672/", redisAddr: "redis:6379", redisPassword: "r3dis",
			wantRedis: RedisConfig{Addr: "redis:6379", Password: "r3dis"}},
		{name: "blank_redis_address_means_no_redis", dsn: dsn, url: "amqp://gostream:s3cret@rabbitmq:5672/", redisAddr: "  ",
			wantRedis: RedisConfig{}},
		{name: "missing_dsn", url: "amqp://gostream:s3cret@rabbitmq:5672/", wantErr: "GOSTREAM_DSN"},
		{name: "missing_url", dsn: dsn, wantErr: "GOSTREAM_AMQP_URL"},
		{name: "wrong_scheme", dsn: dsn, url: "http://gostream:s3cret@rabbitmq:5672/", wantErr: "GOSTREAM_AMQP_URL"},
		{name: "no_host", dsn: dsn, url: "amqp://gostream:s3cret@:5672/", wantErr: "GOSTREAM_AMQP_URL"},
		{name: "unparsable", dsn: dsn, url: "amqp://gostream:s3cret@rabbit mq:5672/%zz", wantErr: "GOSTREAM_AMQP_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOSTREAM_DSN", tc.dsn)
			t.Setenv("GOSTREAM_AMQP_URL", tc.url)
			t.Setenv("GOSTREAM_REDIS_ADDR", tc.redisAddr)
			t.Setenv("GOSTREAM_REDIS_PASSWORD", tc.redisPassword)
			// The worker must not need the API's signing secrets.
			t.Setenv("JWT_SECRET", "")
			t.Setenv("GOSTREAM_FEED_CURSOR_SECRET", "")
			cfg, err := LoadWorker()
			if tc.wantErr == "" {
				if err != nil || cfg.DSN != tc.dsn || cfg.AMQPURL != tc.url || cfg.Redis.Addr != tc.wantRedis.Addr ||
					tc.wantRedis.Addr != "" && cfg.Redis.Password != tc.wantRedis.Password {
					t.Fatalf("got DSN %t, AMQP URL %t, Redis address %q, err %v", cfg.DSN == tc.dsn, cfg.AMQPURL == tc.url, cfg.Redis.Addr, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error about %s, got %v", tc.wantErr, err)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Fatal("errors must not repeat the password")
			}
			if cfg != (WorkerConfig{}) {
				t.Fatal("a failed load must return no configuration")
			}
		})
	}
}
