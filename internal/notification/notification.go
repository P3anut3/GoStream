// Package notification 把点赞、评论事件变成视频作者能看到的通知，并提供通知列表、未读数和已读。
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

// 通知列表和已读接口的错误，内容会直接返回给前端。
var (
	ErrNotFound      = errors.New("通知不存在")
	ErrInvalidLimit  = errors.New("limit 必须是 1～50 的整数")
	ErrInvalidCursor = errors.New("cursor 无效，请刷新列表")
)

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

// Item 是通知列表里的一条，json 标签就是返回给前端的字段名。
// 两个 gorm 标签说明查询结果里对应的列名（列名和字段名对不上的才需要写）。
type Item struct {
	ID             uint64    `json:"id"`
	Type           string    `json:"type" gorm:"column:event_type"`
	ActorID        uint64    `json:"actor_id"`
	ActorName      string    `json:"actor_name"`
	VideoID        uint64    `json:"video_id"`
	VideoTitle     string    `json:"video_title"`
	CommentID      *uint64   `json:"comment_id"`      // 点赞通知是 null
	CommentContent *string   `json:"comment_content"` // 点赞通知、评论已经删除时是 null
	CreatedAt      time.Time `json:"created_at"`
	Read           bool      `json:"read" gorm:"column:is_read"`
}

// Page 是一页通知。NextCursor 是下一页的起点，没有下一页时是空字符串。
type Page struct {
	Notifications []Item `json:"notifications"`
	NextCursor    string `json:"next_cursor"`
	HasMore       bool   `json:"has_more"`
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
