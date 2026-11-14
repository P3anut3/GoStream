package notification

import (
	"context"
	"encoding/json"
	"log"

	"github.com/redis/go-redis/v9"
)

// SignalChannel 是 worker 告诉 API "某个用户有新通知"的 Redis 频道。
const SignalChannel = "gostream:notifications:v1"

// signal 是频道里的一条消息，只说明收通知的人，通知内容在数据库里。
type signal struct {
	RecipientID uint64 `json:"recipient_id"`
}

// RedisSignals 用 Redis 发布订阅传提示：worker 写完通知后 Publish，每个 API 进程 Listen。
// 发布订阅不保存消息，没人订阅时发的提示直接丢掉；这没关系，提示只是让页面去查一下。
type RedisSignals struct {
	Client *redis.Client
}

// Publish 告诉所有 API 进程：recipientID 有新通知了。
func (s *RedisSignals) Publish(ctx context.Context, recipientID uint64) error {
	payload, err := json.Marshal(signal{RecipientID: recipientID})
	if err != nil {
		return err
	}
	return s.Client.Publish(ctx, SignalChannel, payload).Err()
}

// Listen 订阅提示频道，把每个提示交给 hub，直到 ctx 结束。和 Redis 的连接断开后，go-redis 会自己
// 重连并重新订阅；每次订阅成功都提示 hub 里所有连接，因为断开期间发的提示收不到了。
func (s *RedisSignals) Listen(ctx context.Context, hub *Hub) {
	pubsub := s.Client.Subscribe(ctx, SignalChannel)
	defer pubsub.Close()
	messages := pubsub.ChannelWithSubscriptions()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				return
			}
			switch m := message.(type) {
			case *redis.Subscription:
				if m.Kind == "subscribe" {
					hub.PublishAll()
				}
			case *redis.Message:
				var got signal
				if err := json.Unmarshal([]byte(m.Payload), &got); err != nil || got.RecipientID == 0 {
					log.Printf("忽略格式不对的通知提示（%d 字节）", len(m.Payload))
					continue
				}
				hub.Publish(got.RecipientID)
			}
		}
	}
}
