package feed

import (
	"context"
	"log"
	"time"

	"golang.org/x/sync/singleflight"
)

type Repository interface {
	ListLatest(ctx context.Context, position *Position, limit int) ([]FeedItem, error)
	ListFollowing(ctx context.Context, viewerID uint64, position *Position, limit int) ([]FeedItem, error)
	LoadMetadata(ctx context.Context, videoIDs, authorIDs []uint64, viewerID uint64) (Metadata, error)
}

// LatestTimeline 是最新流的 Redis 时间线；RedisTimeline 是它的实现。
type LatestTimeline interface {
	// Page 返回 position 之后最多 n 条视频；PageState 说明 Redis 能不能回答这一页。
	Page(ctx context.Context, position *Position, n int) ([]FeedItem, PageState, error)
	// Rebuild 用 MySQL 最新的视频（按时间从新到旧，最多 Window()+1 条）重建时间线。
	Rebuild(ctx context.Context, newest []FeedItem) error
	Invalidate(ctx context.Context) error
	Window() int
}

// rebuildTimeout 是一次重建（读 MySQL、写 Redis）的上限；它不跟随某一个调用方取消。
const rebuildTimeout = 3 * time.Second

type Service struct {
	repository Repository
	cursor     *CursorCodec
	timeline   LatestTimeline     // nil 表示最新流直接查 MySQL
	rebuilds   singleflight.Group // 合并同一时刻的时间线重建（只在本进程内有效）
}

func NewService(repository Repository, cursor *CursorCodec, timeline LatestTimeline) *Service {
	return &Service{
		repository: repository,
		cursor:     cursor,
		timeline:   timeline,
	}
}

func (s *Service) ListLatest(ctx context.Context, limit int, token string) (ListResponse, error) {
	return s.ListLatestForViewer(ctx, limit, token, 0)
}

// ListLatestForViewer 使用经过认证的浏览者身份补全个人状态；0 表示游客。
// 这一页的视频先问 Redis 时间线，回答不了再查 MySQL；点赞等个人状态照旧每次查 MySQL。
func (s *Service) ListLatestForViewer(ctx context.Context, limit int, token string, viewerID uint64) (ListResponse, error) {
	return s.list(ctx, KindLatest, limit, token, viewerID, func(position *Position, size int) ([]FeedItem, error) {
		if items, ok := s.latestFromTimeline(ctx, position, size); ok {
			return items, nil
		}
		return s.repository.ListLatest(ctx, position, size)
	})
}

// ListFollowing 只返回 viewerID 关注的作者的作品；viewerID 必须来自已验证的 JWT。
func (s *Service) ListFollowing(ctx context.Context, limit int, token string, viewerID uint64) (ListResponse, error) {
	return s.list(ctx, KindFollowing, limit, token, viewerID, func(position *Position, size int) ([]FeedItem, error) {
		return s.repository.ListFollowing(ctx, viewerID, position, size)
	})
}

// latestFromTimeline 先问 Redis 时间线；ok 为 false 时，调用方改查 MySQL。
func (s *Service) latestFromTimeline(ctx context.Context, position *Position, size int) ([]FeedItem, bool) {
	if s.timeline == nil {
		return nil, false
	}
	items, state, err := s.timeline.Page(ctx, position, size)
	if err == nil && state == PageMissing {
		// 时间线不存在或不完整：重建一次再读。
		if err = s.rebuildLatest(ctx); err == nil {
			items, state, err = s.timeline.Page(ctx, position, size)
		}
	}
	if err != nil {
		log.Printf("读取最新流时间线失败，改为查询数据库: %v", err)
		return nil, false
	}
	// 超出窗口（PageBeyond）或重建后仍不完整，都回 MySQL。
	return items, state == PageHit
}

// rebuildLatest 从 MySQL 读最新的一批视频写回 Redis；同一时刻只重建一次，其余请求等结果。
func (s *Service) rebuildLatest(ctx context.Context) error {
	result := s.rebuilds.DoChan("latest", func() (any, error) {
		// 这次重建由所有等待者共享：不能因为第一个调用方断开，就让其他人一起失败。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rebuildTimeout)
		defer cancel()
		// 多读一条：读到 Window()+1 条，说明窗口外还有更早的视频。
		newest, err := s.repository.ListLatest(loadCtx, nil, s.timeline.Window()+1)
		if err != nil {
			return nil, err
		}
		return nil, s.timeline.Rebuild(loadCtx, newest)
	})
	select {
	case r := <-result:
		return r.Err
	case <-ctx.Done():
		return ctx.Err() // 只有当前调用方放弃等待，重建继续进行
	}
}

// InvalidateLatest 在作品发布或删除成功后调用：删掉整条时间线，下一次读取时重建。
func (s *Service) InvalidateLatest(ctx context.Context) {
	if s.timeline == nil {
		return
	}
	if err := s.timeline.Invalidate(ctx); err != nil {
		log.Printf("删除最新流时间线失败，旧时间线会在过期后消失: %v", err)
	}
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
