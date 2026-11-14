// cmd/worker 是和 API 分开运行的后台进程，同时做两件事：
//   - 投递员：把点赞、评论写进发件箱的事件发到 RabbitMQ；
//   - 消费者：从通知队列取出事件，写成视频作者的通知，再经 Redis 提示 API "这个作者有新通知"。
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
	"github.com/redis/go-redis/v9"
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
	signals, closeSignals := newSignals(cfg.Redis)
	defer closeSignals()
	consumer := &mq.Consumer{URL: cfg.AMQPURL, Handler: handleEvent(notifications, signals)}
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

// newSignals 返回给 API 发"有新通知"提示的工具，和用完以后关闭它的函数。
// 没配置 Redis 时返回无类型的 nil 接口：把 nil 指针装进接口的话，signals != nil 也会成立，
// handleEvent 就会去用一个不存在的客户端。
func newSignals(cfg config.RedisConfig) (signaler, func()) {
	if cfg.Addr == "" {
		log.Printf("未配置 Redis：通知照常写入，但不提示 API，页面要等下一次轮询（最多 60 秒）才看到")
		return nil, func() {}
	}
	client := newRedisClient(cfg)
	return &notification.RedisSignals{Client: client}, func() { client.Close() }
}

// newRedisClient 和 API 的配置原则一样：提示只是锦上添花，超时要短、失败不重试，不能拖慢消费。
func newRedisClient(cfg config.RedisConfig) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:          cfg.Addr,
		Password:      cfg.Password,
		DialTimeout:   time.Second,
		ReadTimeout:   300 * time.Millisecond,
		WriteTimeout:  300 * time.Millisecond,
		MaxRetries:    -1, // 命令失败不重试
		DialerRetries: 1,  // 建连也只试一次（<= 0 会按默认的 5 次处理）
	})
}

// signaler 是 handleEvent 发提示需要的能力；notification.RedisSignals 实现了它，测试里换成假的。
type signaler interface {
	Publish(ctx context.Context, recipientID uint64) error
}

// handleEvent 把通知服务接到消费者上：事件本身有问题（notification.ErrInvalidEvent）时，
// 让消费者拒绝这条消息、移进死信队列；其他错误原样返回，消费者会稍后重试。
// 通知写好以后再提示 API（signals 为 nil 时不提示）：浏览器收到提示来查时，一定查得到这条通知。
// 这样 notification 包不需要知道 RabbitMQ，mq 包也不需要知道通知。
func handleEvent(s *notification.Service, signals signaler) mq.Handler {
	return func(ctx context.Context, e outbox.Event) error {
		err := s.HandleEvent(ctx, e)
		if errors.Is(err, notification.ErrInvalidEvent) {
			return fmt.Errorf("%w: %w", mq.ErrReject, err)
		}
		if err != nil {
			return err
		}
		if signals != nil {
			notify(ctx, signals, e)
		}
		return nil
	}
}

// notify 告诉 API：这个事件的接收者有新通知了。通知已经写进数据库，所以发送失败只记日志、照常确认消息：
// 重试也只会重复写同一条通知；页面下一次轮询或重新连接时一样能看到它。
func notify(ctx context.Context, signals signaler, e outbox.Event) {
	n, err := notification.FromEvent(e)
	if err != nil {
		return // HandleEvent 刚刚接受了这个事件，不会走到这里
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := signals.Publish(ctx, n.RecipientID); err != nil {
		log.Printf("提示 API 有新通知失败（页面会在下一次轮询或重连时看到）: %v", err)
	}
}
