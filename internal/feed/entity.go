package feed

import "time"

// ListRequest 接收 HTTP 查询参数；Cursor 是前端原样回传的字符串。
type ListRequest struct {
	Limit  int    `form:"limit"`
	Cursor string `form:"cursor"`
}

// FeedItem 是公开的展示数据，不包含数据库或上传生命周期的内部信息。
type FeedItem struct {
	ID         uint64    `json:"id"`
	AuthorID   uint64    `json:"author_id"`
	Title      string    `json:"title"`
	PlayURL    string    `json:"play_url"`
	CoverURL   string    `json:"cover_url"`
	CreatedAt  time.Time `json:"created_at"`
	AuthorName string    `json:"author_name"`
	LikeCount  int64     `json:"like_count"`
	Liked      bool      `json:"liked"`
}

type LikeSummary struct {
	VideoID   uint64
	LikeCount int64
	Liked     bool
}

// Metadata 只包含当前批次的展示信息；个人点赞状态不应进入公共缓存。
type Metadata struct {
	Authors map[uint64]string
	Likes   map[uint64]LikeSummary
}

// ListResponse 返回一批视频以及继续加载所需的信息。
type ListResponse struct {
	Videos     []FeedItem `json:"videos"`
	NextCursor string     `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}

// Position 是服务端内部的续读位置，不直接作为 HTTP 响应返回。
// 查询时以 CreatedAt、ID 的组合确定唯一的排序位置。
type Position struct {
	CreatedAt time.Time
	ID        uint64
}
