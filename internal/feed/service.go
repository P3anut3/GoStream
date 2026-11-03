package feed

import (
	"context"
)

type Repository interface {
	ListLatest(ctx context.Context, position *Position, limit int) ([]FeedItem, error)
	ListFollowing(ctx context.Context, viewerID uint64, position *Position, limit int) ([]FeedItem, error)
	LoadMetadata(ctx context.Context, videoIDs, authorIDs []uint64, viewerID uint64) (Metadata, error)
}

type Service struct {
	repository Repository
	cursor     *CursorCodec
}

func NewService(repository Repository, cursor *CursorCodec) *Service {
	return &Service{
		repository: repository,
		cursor:     cursor,
	}
}

func (s *Service) ListLatest(ctx context.Context, limit int, token string) (ListResponse, error) {
	return s.ListLatestForViewer(ctx, limit, token, 0)
}

// ListLatestForViewer 使用经过认证的浏览者身份补全个人状态；0 表示游客。
func (s *Service) ListLatestForViewer(ctx context.Context, limit int, token string, viewerID uint64) (ListResponse, error) {
	return s.list(ctx, KindLatest, limit, token, viewerID, func(position *Position, size int) ([]FeedItem, error) {
		return s.repository.ListLatest(ctx, position, size)
	})
}

// ListFollowing 只返回 viewerID 关注的作者的作品；viewerID 必须来自已验证的 JWT。
func (s *Service) ListFollowing(ctx context.Context, limit int, token string, viewerID uint64) (ListResponse, error) {
	return s.list(ctx, KindFollowing, limit, token, viewerID, func(position *Position, size int) ([]FeedItem, error) {
		return s.repository.ListFollowing(ctx, viewerID, position, size)
	})
}

// list 是各种视频流共用的分页流程；fetch 只决定"从哪里取作品"，其余步骤完全相同。
func (s *Service) list(ctx context.Context, kind Kind, limit int, token string, viewerID uint64, fetch func(position *Position, size int) ([]FeedItem, error)) (ListResponse, error) {
	// 1、规范化每批条数
	if limit < 0 {
		return ListResponse{}, ErrInvalidLimit
	}
	if limit == 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	// 2、将前端传回的字符串还原成查询位置；游标必须属于当前这种流
	position, err := s.cursor.Decode(kind, token)
	if err != nil {
		return ListResponse{}, ErrInvalidCursor
	}

	// 3、多查询一条，用于判断后面是否还有数据
	items, err := fetch(position, limit+1)
	if err != nil {
		return ListResponse{}, err
	}
	// 4、只返回前端请求条数
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	// 保证空列表最终输出为[]
	if items == nil {
		items = make([]FeedItem, 0)
	}
	if len(items) > 0 {
		videoIDs, authorIDs := make([]uint64, 0, len(items)), make([]uint64, 0, len(items))
		seenAuthors := make(map[uint64]bool)
		for _, item := range items {
			videoIDs = append(videoIDs, item.ID)
			if !seenAuthors[item.AuthorID] {
				seenAuthors[item.AuthorID] = true
				authorIDs = append(authorIDs, item.AuthorID)
			}
		}
		metadata, err := s.repository.LoadMetadata(ctx, videoIDs, authorIDs, viewerID)
		if err != nil {
			return ListResponse{}, err
		}
		for i := range items {
			items[i].AuthorName = metadata.Authors[items[i].AuthorID]
			items[i].LikeCount = metadata.Likes[items[i].ID].LikeCount
			items[i].Liked = viewerID > 0 && metadata.Likes[items[i].ID].Liked
		}
	}
	response := ListResponse{
		Videos:  items,
		HasMore: hasMore,
	}
	// 5、用实际返回的最后一条视频，生成同一种流的下一游标
	if hasMore {
		last := items[len(items)-1]
		nextCursor, err := s.cursor.Encode(kind, Position{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		})
		if err != nil {
			return ListResponse{}, err
		}
		response.NextCursor = nextCursor
	}
	return response, nil
}
