// Package mq 负责和 RabbitMQ 打交道：声明交换机和队列，把发件箱的事件发出去。
package mq

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"gostream/internal/outbox"
)

// 交换机和队列的名字。
const (
	// EventsExchange 收所有业务事件，按 routing key（事件类型）分发到各个队列。
	EventsExchange = "gostream.events"
	// NotificationsQueue 是通知队列：第 3 步的消费者从这里取点赞、评论事件，写成通知。
	NotificationsQueue = "gostream.notifications"
	// DeadLetterExchange 和 DeadLetterQueue 隔离通知队列里处理不了的消息（第 3 步使用）。
	DeadLetterExchange = "gostream.events.dead"
	DeadLetterQueue    = "gostream.notifications.dead"
	// MaxDeliveries：一条消息最多投递这么多次，还没被处理掉就移进死信队列。
	MaxDeliveries = 5
)

// NotificationEventTypes 是通知队列接收的事件类型，也就是它绑定的 routing key。
// 新增事件类型时要加在这里，否则发布会被退回、留在发件箱里。
var NotificationEventTypes = []string{outbox.TypeLikeCreated, outbox.TypeCommentCreated}

// DeclareTopology 声明交换机、通知队列和死信队列，并把它们绑在一起。
// 声明是幂等的：已经存在且参数相同就什么都不做，所以每次连接时都可以调用。
// 队列的参数建好以后就改不了（参数不同会报 PRECONDITION_FAILED），所以死信设置在这一步一起建好。
func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(EventsExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明交换机 %s 失败: %w", EventsExchange, err)
	}
	if err := ch.ExchangeDeclare(DeadLetterExchange, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明交换机 %s 失败: %w", DeadLetterExchange, err)
	}
	if _, err := ch.QueueDeclare(DeadLetterQueue, true, false, false, false, amqp.Table{
		amqp.QueueTypeArg: amqp.QueueTypeQuorum,
	}); err != nil {
		return fmt.Errorf("声明队列 %s 失败: %w", DeadLetterQueue, err)
	}
	if err := ch.QueueBind(DeadLetterQueue, "", DeadLetterExchange, false, nil); err != nil {
		return fmt.Errorf("绑定队列 %s 失败: %w", DeadLetterQueue, err)
	}
	// quorum 队列：数据写进磁盘并且可以多节点复制，重启不丢；超过投递次数的消息进死信交换机。
	if _, err := ch.QueueDeclare(NotificationsQueue, true, false, false, false, amqp.Table{
		amqp.QueueTypeArg:        amqp.QueueTypeQuorum,
		"x-dead-letter-exchange": DeadLetterExchange,
		"x-delivery-limit":       MaxDeliveries,
	}); err != nil {
		return fmt.Errorf("声明队列 %s 失败: %w", NotificationsQueue, err)
	}
	for _, eventType := range NotificationEventTypes {
		if err := ch.QueueBind(NotificationsQueue, eventType, EventsExchange, false, nil); err != nil {
			return fmt.Errorf("绑定 %s 到队列 %s 失败: %w", eventType, NotificationsQueue, err)
		}
	}
	return nil
}
