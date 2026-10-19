package video

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidCommentContent = errors.New("评论内容必须为 1~500 个字符")
	ErrCommentVideoNotFound  = errors.New("视频不存在")
)

// CommentRepository describes only the database operation this service needs.
type CommentRepository interface {
	CreateComment(ctx context.Context, userID, videoID uint64, content string) (uint64, error)
}

type CreatedComment struct {
	ID      uint64
	VideoID uint64
	UserID  uint64
	Content string
}

type CommentService struct {
	repository CommentRepository
}

func NewCommentService(repository CommentRepository) *CommentService {
	return &CommentService{repository: repository}
}

// CreateComment receives ordinary Go values, without depending on HTTP or Hertz.
func (s *CommentService) CreateComment(ctx context.Context, userID, videoID uint64, content string) (CreatedComment, error) {
	content = strings.TrimSpace(content)
	length := utf8.RuneCountInString(content)
	if length < 1 || length > 500 {
		return CreatedComment{}, ErrInvalidCommentContent
	}
	id, err := s.repository.CreateComment(ctx, userID, videoID, content)
	if err != nil {
		return CreatedComment{}, err
	}
	return CreatedComment{ID: id, VideoID: videoID, UserID: userID, Content: content}, nil
}
