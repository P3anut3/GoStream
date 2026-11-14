// Package notification 把点赞、评论事件变成视频作者能看到的通知。
package notification

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gostream/internal/outbox"
)

// ErrInvalidEvent 表示事件本身有问题：类型不认识、内容解析不了、缺少 ID……
// 这种事件重试多少次都不会成功，worker 收到它会拒绝这条消息，让它进死信队列。
var ErrInvalidEvent = errors.New("事件无效")

// Notification 是通知表里的一行。
type Notification struct {
	RecipientID uint64    // 收通知的人：视频作者
	ActorID     uint64    // 点赞、评论的人
	EventID     uint64    // 发件箱里的事件 ID：同一接收者同一事件只有一条通知
	EventType   string    // outbox.TypeLikeCreated 或 outbox.TypeCommentCreated
	VideoID     uint64    // 被点赞、评论的视频
	CommentID   *uint64   // 只有评论通知有；点赞通知是 nil，写进数据库就是 NULL
	CreatedAt   time.Time // 事件发生的时间，不是处理它的时间
}

// FromEvent 把一个事件转换成发给视频作者的通知。
// 事件有问题时，返回的错误包含 ErrInvalidEvent，可以用 errors.Is 判断。
func FromEvent(e outbox.Event) (Notification, error) {
	n := Notification{EventID: e.ID, EventType: e.Type, CreatedAt: e.CreatedAt}
	switch e.Type {
	case outbox.TypeLikeCreated:
		var p outbox.LikeCreated
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return Notification{}, fmt.Errorf("%w: 事件 %d 的内容无法解析: %v", ErrInvalidEvent, e.ID, err)
		}
		n.RecipientID, n.ActorID, n.VideoID = p.AuthorID, p.ActorID, p.VideoID
	case outbox.TypeCommentCreated:
		var p outbox.CommentCreated
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return Notification{}, fmt.Errorf("%w: 事件 %d 的内容无法解析: %v", ErrInvalidEvent, e.ID, err)
		}
		if p.CommentID == 0 {
			return Notification{}, fmt.Errorf("%w: 评论事件 %d 缺少 comment_id", ErrInvalidEvent, e.ID)
		}
		n.RecipientID, n.ActorID, n.VideoID = p.AuthorID, p.ActorID, p.VideoID
		n.CommentID = &p.CommentID
	default:
		return Notification{}, fmt.Errorf("%w: 不认识的事件类型 %q", ErrInvalidEvent, e.Type)
	}
	if n.EventID == 0 || n.RecipientID == 0 || n.ActorID == 0 || n.VideoID == 0 || n.CreatedAt.IsZero() {
		return Notification{}, fmt.Errorf("%w: 事件 %d 缺少 ID 或时间", ErrInvalidEvent, e.ID)
	}
	// 第一步写事件时已经排除了自己给自己点赞、评论；这里还出现，说明事件不对。
	if n.ActorID == n.RecipientID {
		return Notification{}, fmt.Errorf("%w: 事件 %d 是自己给自己的", ErrInvalidEvent, e.ID)
	}
	return n, nil
}
