package outbox

// 事件类型。第 2 步发到 RabbitMQ 时，它也是消息的 routing key。
const (
	TypeLikeCreated    = "like.created"
	TypeCommentCreated = "comment.created"
)

// LikeCreated: ActorID 给 AuthorID 的视频点赞了
// 事件只记录 ID：用户名、标题这些会变化的内容，展示通知时再查
type LikeCreated struct {
	ActorID  uint64 `json:"actor_id"`
	AuthorID uint64 `json:"author_id"` //视频作者，也就是接收通知的人
	VideoID  uint64 `json:"video_id"`
}

// CommentCreated: ActorID 评论了 AuthorID 的视频。
type CommentCreated struct {
	ActorID   uint64 `json:"actor_id"`
	AuthorID  uint64 `json:"author_id"`
	VideoID   uint64 `json:"video_id"`
	CommentID uint64 `json:"comment_id"`
}
