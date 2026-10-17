package video

import "time"

type Video struct {
	ID        uint64    `json:"id"`
	AuthorID  uint64    `json:"author_id"`
	Title     string    `json:"title"`
	PlayURL   string    `json:"play_url"`
	CoverURL  string    `json:"cover_url"`
	CreatedAt time.Time `json:"created_at"`
}
