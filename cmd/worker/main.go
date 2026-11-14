// cmd/worker 是和 API 分开运行的后台进程，同时做两件事：
//   - 投递员：把点赞、评论写进发件箱的事件发到 RabbitMQ；
//   - 消费者：从通知队列取出事件，写成视频作者的通知。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gostream/internal/config"
	"gostream/internal/database"
	"gostream/internal/mq"
	"gostream/internal/notification"
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
	// Ctrl+C 或 docker stop（SIGTERM）会取消 ctx，投递员和消费者做完手头的事就退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, nil); err != nil {
		log.Fatal(err)
	}
}

// run 连接数据库，然后同时运行投递员和消费者，直到 ctx 被取消、两个都停下来。
// tune 让测试可以缩短等待时间，正式运行时为 nil。
func run(ctx context.Context, cfg config.WorkerConfig, tune func(*outbox.Relay, *mq.Consumer)) error {
	db, err := database.OpenGORM(cfg.DSN)
	if err != nil {
		return errors.New("连接数据库失败，请检查配置和 MySQL 服务")
	}
	if pool, err := db.DB(); err == nil {
		defer pool.Close()
	}
	publisher := &mq.Publisher{URL: cfg.AMQPURL, Timeout: 5 * time.Second}
	defer publisher.Close()
	relay := outbox.NewRelay(db, publisher)
	notifications := notification.NewService(&notification.GORMRepository{DB: db})
	consumer := &mq.Consumer{URL: cfg.AMQPURL, Handler: handleEvent(notifications)}
	if tune != nil {
		tune(relay, consumer)
	}

	log.Printf("GoStream worker 已启动：投递发件箱里的事件，把通知队列里的事件写成通知")
	var wg sync.WaitGroup
	wg.Go(func() { relay.Run(ctx) }) // wg.Go 启动一个 goroutine，并让 wg.Wait 等它结束
	wg.Go(func() { consumer.Run(ctx) })
	wg.Wait()
	log.Printf("GoStream worker 已停止")
	return nil
}

// handleEvent 把通知服务接到消费者上：事件本身有问题（notification.ErrInvalidEvent）时，
// 让消费者拒绝这条消息、移进死信队列；其他错误原样返回，消费者会稍后重试。
// 这样 notification 包不需要知道 RabbitMQ，mq 包也不需要知道通知。
func handleEvent(s *notification.Service) mq.Handler {
	return func(ctx context.Context, e outbox.Event) error {
		err := s.HandleEvent(ctx, e)
		if errors.Is(err, notification.ErrInvalidEvent) {
			return fmt.Errorf("%w: %w", mq.ErrReject, err)
		}
		return err
	}
}
