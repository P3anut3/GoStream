package mq

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gostream/internal/outbox"
)

// ErrReject 表示这条消息处理不了，重试也没用：消费者拒绝它，RabbitMQ 把它移进死信队列。
// Handler 返回的错误用 errors.Is 能判断出 ErrReject 时生效。
var ErrReject = errors.New("消息无法处理")

// Handler 处理一条消息还原出来的事件。
//   - 返回 nil：处理完了，确认（ACK），RabbitMQ 删除这条消息；
//   - 返回包含 ErrReject 的错误：拒绝，消息进死信队列；
//   - 返回其他错误（比如数据库连不上）：消息本身没问题，等一会儿再处理同一条，不退回队列。
type Handler func(ctx context.Context, e outbox.Event) error

// Consumer 从通知队列取消息交给 Handler，处理完一条确认一条。
// RabbitMQ 连不上或者连接断了，它等一会儿重新连接，不会退出。
type Consumer struct {
	URL        string        // amqp://用户名:密码@主机:端口/，里面有密码，不能打印
	Handler    Handler       // 处理每条消息
	Prefetch   int           // 最多同时拿着多少条还没确认的消息；为 0 时用 10
	MaxBackoff time.Duration // 重连、重试最长等多久；为 0 时用 30 秒
	Timeout    time.Duration // 建立连接最多等多久；为 0 时用 5 秒
}

// Run 一直消费，直到 ctx 被取消（收到停止信号）才返回。
// 连不上或者断开时等 1、2、4……秒再连，最长 MaxBackoff；订阅成功过一次，就从头开始算。
func (c *Consumer) Run(ctx context.Context) {
	backoff := c.firstBackoff()
	for {
		subscribed, err := c.consume(ctx)
		if ctx.Err() != nil {
			return // 收到停止信号：还没确认的消息随连接关闭回到队列，下次启动再处理
		}
		if subscribed {
			backoff = c.firstBackoff()
		}
		log.Printf("消费通知队列中断: %v，%v 后重连", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.maxBackoff())
	}
}

// consume 连接 RabbitMQ、订阅通知队列，然后一条一条处理，直到连接出问题或 ctx 被取消。
// subscribed 表示这次订阅成功过。返回时关闭连接，还没确认的消息会回到队列。
func (c *Consumer) consume(ctx context.Context) (subscribed bool, err error) {
	conn, err := amqp.DialConfig(c.URL, amqp.Config{Dial: amqp.DefaultDial(c.timeout())})
	if err != nil {
		return false, fmt.Errorf("连接 RabbitMQ 失败: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return false, fmt.Errorf("打开 RabbitMQ 通道失败: %w", err)
	}
	if err := DeclareTopology(ch); err != nil {
		return false, err
	}
	// prefetch：RabbitMQ 最多先推这么多条还没确认的消息过来，确认一条再补一条。
	if err := ch.Qos(c.prefetch(), 0, false); err != nil {
		return false, fmt.Errorf("设置 prefetch 失败: %w", err)
	}
	// autoAck 为 false：处理完由我们确认；没确认的消息在连接断开后回到队列，不会丢。
	deliveries, err := ch.Consume(NotificationsQueue, "", false, false, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("订阅队列 %s 失败: %w", NotificationsQueue, err)
	}
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case d, open := <-deliveries:
			if !open {
				return true, errors.New("RabbitMQ 关闭了连接或取消了订阅")
			}
			if err := c.handle(ctx, d); err != nil {
				return true, err
			}
		}
	}
}

// handle 处理一条消息，直到确认、拒绝，或者 ctx 被取消。
// 返回错误表示确认或拒绝没有送到（通道断了），需要重新连接；RabbitMQ 会重新投递这条消息。
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) error {
	event, err := toEvent(d)
	if err != nil {
		log.Printf("拒绝消息，移进死信队列: %v", err)
		if err := d.Reject(false); err != nil {
			return fmt.Errorf("拒绝消息失败: %w", err)
		}
		return nil
	}
	backoff := c.firstBackoff()
	for {
		err := c.Handler(ctx, event)
		switch {
		case err == nil:
			// 先处理完（数据库已经提交），再确认：确认之前进程挂了，消息会重新投递，不会丢。
			if err := d.Ack(false); err != nil {
				return fmt.Errorf("确认事件 %d 失败: %w", event.ID, err)
			}
			return nil
		case errors.Is(err, ErrReject):
			log.Printf("事件 %d 无法处理，移进死信队列: %v", event.ID, err)
			if err := d.Reject(false); err != nil {
				return fmt.Errorf("拒绝事件 %d 失败: %w", event.ID, err)
			}
			return nil
		case ctx.Err() != nil:
			return nil // 收到停止信号：不确认，连接关闭后消息回到队列
		}
		// 暂时的错误：消息拿在手里，等一会儿再试同一条。不退回队列，因为退回一次就算一次投递，
		// 数据库停机几秒钟，就可能把没问题的消息送进死信队列。
		log.Printf("处理事件 %d 失败: %v，%v 后重试", event.ID, err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.maxBackoff())
	}
}

// toEvent 把消息还原成发件箱事件：message_id 是事件 ID，type 是事件类型，消息体是事件内容，
// timestamp 是事件发生的时间。message_id 不是事件 ID 的消息无法处理。
// 消息没带时间，或者时间比现在晚一小时以上（时钟明显不对），就用现在的时间；
// 两台机器的时钟差几秒很正常，这种情况保留消息里的时间。
func toEvent(d amqp.Delivery) (outbox.Event, error) {
	id, err := strconv.ParseUint(d.MessageId, 10, 64)
	if err != nil || id == 0 {
		return outbox.Event{}, fmt.Errorf("message_id %q 不是事件 ID", d.MessageId)
	}
	createdAt, now := d.Timestamp, time.Now()
	if createdAt.Unix() <= 0 || createdAt.After(now.Add(time.Hour)) {
		createdAt = now
	}
	return outbox.Event{ID: id, Type: d.Type, Payload: d.Body, CreatedAt: createdAt}, nil
}

func (c *Consumer) prefetch() int {
	if c.Prefetch <= 0 {
		return 10
	}
	return c.Prefetch
}

func (c *Consumer) maxBackoff() time.Duration {
	if c.MaxBackoff <= 0 {
		return 30 * time.Second
	}
	return c.MaxBackoff
}

// firstBackoff 是第一次等待的时间：1 秒；MaxBackoff 比 1 秒还短时用 MaxBackoff（测试里用）。
func (c *Consumer) firstBackoff() time.Duration {
	return min(time.Second, c.maxBackoff())
}

func (c *Consumer) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 5 * time.Second
	}
	return c.Timeout
}
