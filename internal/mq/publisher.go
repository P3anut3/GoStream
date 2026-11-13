package mq

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gostream/internal/outbox"
)

// Publisher 把发件箱的事件发到 RabbitMQ。它实现 outbox.Publisher：
// 只报告 RabbitMQ 确认收到、并且有队列接收的事件，投递员只给这些事件打勾。
// 连接断了不用管，下一次发送时会重新连接、重新声明交换机和队列。
type Publisher struct {
	URL     string        // amqp://用户名:密码@主机:端口/，里面有密码，不能打印
	Timeout time.Duration // 建立连接、等一批确认各自最多等多久；为 0 时用 5 秒

	mu      sync.Mutex
	conn    *amqp.Connection
	ch      *amqp.Channel
	returns chan amqp.Return
}

// Publish 发出一批事件，返回确认收到的事件 ID。
// 每条消息都是持久化的，message_id 是发件箱里的事件 ID（消费者靠它去重），routing key 是事件类型。
// mandatory 为 true：没有队列接收的消息会被退回，不会被 RabbitMQ 悄悄丢掉。
func (p *Publisher) Publish(ctx context.Context, events []outbox.Event) ([]uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.connect(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	var failure error
	confirmations := make([]*amqp.DeferredConfirmation, 0, len(events))
	for _, event := range events {
		confirmation, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, EventsExchange, event.Type, true, false, amqp.Publishing{
			MessageId:    strconv.FormatUint(event.ID, 10),
			Type:         event.Type,
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Timestamp:    event.CreatedAt,
			Body:         event.Payload,
		})
		if err != nil {
			failure = fmt.Errorf("发布事件 %d 失败: %w", event.ID, err)
			break
		}
		confirmations = append(confirmations, confirmation)
	}

	acked := make([]uint64, 0, len(confirmations))
	for i, confirmation := range confirmations {
		ok, err := confirmation.WaitContext(ctx)
		if err != nil {
			failure = errors.Join(failure, fmt.Errorf("等待事件 %d 的确认超时: %w", events[i].ID, err))
			break
		}
		if !ok {
			failure = errors.Join(failure, fmt.Errorf("RabbitMQ 没有确认收到事件 %d（拒收或连接断开）", events[i].ID))
			continue
		}
		acked = append(acked, events[i].ID)
	}

	// 找不到队列的 mandatory 消息，RabbitMQ 会先退回、再确认，所以等完确认以后，退回的消息都已经到了。
	returned := make(map[string]bool)
	for drained := false; !drained; {
		select {
		case message, open := <-p.returns:
			if !open { // 通道已经关闭
				drained = true
				break
			}
			returned[message.MessageId] = true
		default:
			drained = true
		}
	}
	confirmed := make([]uint64, 0, len(acked))
	for _, id := range acked {
		if returned[strconv.FormatUint(id, 10)] {
			failure = errors.Join(failure, fmt.Errorf("事件 %d 没有队列接收，已被 RabbitMQ 退回", id))
			continue
		}
		confirmed = append(confirmed, id)
	}
	if failure != nil {
		// 出过错的通道可能还留着迟到的确认或退回，关掉它，下一批用新通道。
		p.close()
	}
	return confirmed, failure
}

// Close 断开和 RabbitMQ 的连接。
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.close()
	return nil
}

func (p *Publisher) timeout() time.Duration {
	if p.Timeout <= 0 {
		return 5 * time.Second
	}
	return p.Timeout
}

// connect 在没有可用通道时建立连接，打开确认模式，并声明交换机和队列。
func (p *Publisher) connect() error {
	if p.ch != nil && !p.ch.IsClosed() {
		return nil
	}
	p.close()
	conn, err := amqp.DialConfig(p.URL, amqp.Config{Dial: amqp.DefaultDial(p.timeout())})
	if err != nil {
		return fmt.Errorf("连接 RabbitMQ 失败: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("打开 RabbitMQ 通道失败: %w", err)
	}
	// 确认模式：RabbitMQ 对每条消息回一个 ack（存下了）或 nack（出问题了）。
	if err := ch.Confirm(false); err != nil {
		conn.Close()
		return fmt.Errorf("开启发布确认失败: %w", err)
	}
	if err := DeclareTopology(ch); err != nil {
		conn.Close()
		return err
	}
	p.conn, p.ch = conn, ch
	// 缓冲要比一批的条数大：读不走退回的消息会卡住客户端，连确认也收不到。
	p.returns = ch.NotifyReturn(make(chan amqp.Return, 1024))
	return nil
}

func (p *Publisher) close() {
	if p.ch != nil {
		_ = p.ch.Close()
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn, p.ch, p.returns = nil, nil, nil
}
