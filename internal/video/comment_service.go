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
	ErrCommentNotFound       = errors.New("评论不存在或不属于当前用户")
)

// CommentRepository describes only the database operation this service needs.
type CommentRepository interface {
	CreateComment(ctx context.Context, userID, videoID uint64, content string) (uint64, error)
	DeleteComment(ctx context.Context, userID, commentID uint64) (bool, error)
	ListComments(ctx context.Context, videoID uint64, limit, offset int) ([]Comment, error)
}

type CreatedComment struct {
	ID      uint64
	VideoID uint64
	UserID  uint64
	Content string
}

type CommentService struct {
	repository CommentRepository // 数据操作
}

func NewCommentService(repository CommentRepository) *CommentService {
	return &CommentService{repository: repository}
}

// Service 处理业务规则，并通过 repository 接口访问数据。
// 当前列表查询没有额外业务规则，因此直接返回查询结果。
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

// 删除评论
func (s *CommentService) DeleteComment(ctx context.Context, userID, commentID uint64) error {
	deleted, err := s.repository.DeleteComment(ctx, userID, commentID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrCommentNotFound
	}
	return nil
}

func (s *CommentService) ListComments(ctx context.Context, videoID uint64, limit, offset int) ([]Comment, error) {
	return s.repository.ListComments(ctx, videoID, limit, offset)
	// 注意service里调用的都是 数据层的函数也就是 repository 而不是s.ListComments
}
