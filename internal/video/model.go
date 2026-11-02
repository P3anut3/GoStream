package video

import "time"

type Video struct {
	ID        uint64    `json:"id"`
	AuthorID  uint64    `json:"author_id"`
	Title     string    `json:"title"`
	PlayURL   string    `json:"play_url"`
	CoverURL  string    `json:"cover_url"`
	CreatedAt time.Time `json:"created_at" gorm:"->"`
}
type Comment struct {
	ID        uint64    `json:"id"`
	VideoID   uint64    `json:"video_id"`
	UserID    uint64    `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at" gorm:"->"`
}

func (Comment) TableName() string { return "video_comments" }
