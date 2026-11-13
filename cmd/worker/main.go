// cmd/worker 是和 API 分开运行的后台进程。现在只做一件事：发件箱投递员，
// 把点赞、评论写进发件箱的事件发到 RabbitMQ。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/gorm"
	"gostream/internal/config"
	"gostream/internal/database"
	"gostream/internal/mq"
	"gostream/internal/outbox"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("加载 .env 失败")
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		log.Fatal(err)
	}
	// Ctrl+C 或 docker stop（SIGTERM）会取消 ctx，投递员做完手头的事就退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, outbox.NewRelay); err != nil {
		log.Fatal(err)
	}
}

// run 连接数据库，然后一直投递，直到 ctx 被取消。newRelay 让测试可以缩短等待时间。
func run(ctx context.Context, cfg config.WorkerConfig, newRelay func(*gorm.DB, outbox.Publisher) *outbox.Relay) error {
	db, err := database.OpenGORM(cfg.DSN)
	if err != nil {
		return errors.New("连接数据库失败，请检查配置和 MySQL 服务")
	}
	if pool, err := db.DB(); err == nil {
		defer pool.Close()
	}
	publisher := &mq.Publisher{URL: cfg.AMQPURL, Timeout: 5 * time.Second}
	defer publisher.Close()

	log.Printf("GoStream worker 已启动：开始投递发件箱里的事件")
	newRelay(db, publisher).Run(ctx)
	log.Printf("GoStream worker 已停止")
	return nil
}
