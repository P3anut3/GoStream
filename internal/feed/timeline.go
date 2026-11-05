package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// timelineIDsKey 是有序集合：成员是补零的视频 ID，分数是发布时间（毫秒），Redis 按分数排好序。
	timelineIDsKey = "gostream:feed:latest:v1:ids"
	// timelineVideosKey 是哈希：字段是补零的视频 ID，值是这个视频公开字段的 JSON。
	timelineVideosKey = "gostream:feed:latest:v1:videos"
	// 两个哨兵的分数都是 0，比任何视频都小，所以永远排在时间线最底部。
	sentinelEnd  = "end"  // 窗口里已经是全部视频：读到它就没有更多了
	sentinelMore = "more" // 窗口只到这里：更早的视频要回 MySQL 查
)

// PageState 说明 Redis 能不能回答这一页。
type PageState int

const (
	PageMissing PageState = iota // 时间线不存在或不完整，需要重建
	PageHit                      // Redis 能完整回答这一页
	PageBeyond                   // 这一页超出了窗口，要回 MySQL 查
)

// RedisTimeline 把最新的一批视频放进 Redis：有序集合管顺序，哈希管内容。
// 它只是加速层：作品发布或删除后整条删掉，下一次读取时再从 MySQL 重建。
type RedisTimeline struct {
	Client *redis.Client
	Size   int           // 窗口大小：最多缓存最新的多少条视频
	TTL    time.Duration // 整条时间线的过期时间，兜底并发重建时可能写回的旧列表
}

// timelineVideo 是缓存的公开字段；作者名、点赞数和"我是否点过赞"每次都查 MySQL。
type timelineVideo struct {
	ID        uint64    `json:"id"`
	AuthorID  uint64    `json:"author_id"`
	Title     string    `json:"title"`
	PlayURL   string    `json:"play_url"`
	CoverURL  string    `json:"cover_url"`
	CreatedAt time.Time `json:"created_at"`
}

// timelineMember 把 ID 补零到 20 位。同一毫秒发布的视频按成员的字典序排，
// 不补零的话 "9" 比 "10" 大，倒序时 9 会排在 10 前面，和 MySQL 的 id DESC 对不上，翻页会漏或重复。
func timelineMember(id uint64) string { return fmt.Sprintf("%020d", id) }

// Window 返回窗口大小；重建时要从 MySQL 多读一条，判断窗口外还有没有更早的视频。
func (t *RedisTimeline) Window() int { return t.Size }

// Page 返回 position 之后最多 n 条视频，顺序和 MySQL 的 ORDER BY created_at DESC, id DESC 一致。
func (t *RedisTimeline) Page(ctx context.Context, position *Position, n int) ([]FeedItem, PageState, error) {
	members, err := t.pageMembers(ctx, position, n)
	if err != nil {
		return nil, PageMissing, fmt.Errorf("读取时间线失败: %w", err)
	}
	ids := make([]string, 0, len(members))
	state := PageMissing // 既没读满一页、也没碰到哨兵：时间线不存在或不完整
	for _, member := range members {
		if member == sentinelEnd {
			state = PageHit
			break
		}
		if member == sentinelMore {
			state = PageBeyond
			break
		}
		ids = append(ids, member)
	}
	if len(ids) == n {
		state = PageHit // 读满了一页，后面还有什么都不影响这一页
	}
	if state != PageHit {
		return nil, state, nil
	}
	if len(ids) == 0 {
		return []FeedItem{}, PageHit, nil // 已经到底了
	}
	values, err := t.Client.HMGet(ctx, timelineVideosKey, ids...).Result()
	if err != nil {
		return nil, PageMissing, fmt.Errorf("读取时间线视频失败: %w", err)
	}
	items := make([]FeedItem, 0, len(values))
	for _, value := range values {
		data, ok := value.(string)
		if !ok {
			return nil, PageMissing, nil // 内容缺了（比如哈希刚好过期）：当作不完整，重建一次
		}
		var video timelineVideo
		if err := json.Unmarshal([]byte(data), &video); err != nil {
			return nil, PageMissing, fmt.Errorf("解析时间线视频失败: %w", err)
		}
		items = append(items, FeedItem{
			ID: video.ID, AuthorID: video.AuthorID, Title: video.Title,
			PlayURL: video.PlayURL, CoverURL: video.CoverURL, CreatedAt: video.CreatedAt,
		})
	}
	return items, PageHit, nil
}

// pageMembers 取出这一页的成员，末尾可能带着哨兵。
func (t *RedisTimeline) pageMembers(ctx context.Context, position *Position, n int) ([]string, error) {
	if position == nil {
		// 第一页：按排名从最新的开始取 n 个。
		return t.Client.ZRangeArgs(ctx, redis.ZRangeArgs{Key: timelineIDsKey, Start: 0, Stop: n - 1, Rev: true}).Result()
	}
	score := strconv.FormatInt(position.CreatedAt.UnixMilli(), 10)
	var same, older *redis.StringSliceCmd
	// 两次查询放进同一个 MULTI，看到的是同一时刻的时间线。
	_, err := t.Client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		// 和游标同一毫秒发布的视频，通常只有一两个。
		same = pipe.ZRangeArgs(ctx, redis.ZRangeArgs{Key: timelineIDsKey, Start: score, Stop: score, ByScore: true, Rev: true})
		// 比游标更早发布的：带 Rev 时 Start 写较大的分数，"(" 表示不包含这个分数本身。
		older = pipe.ZRangeArgs(ctx, redis.ZRangeArgs{Key: timelineIDsKey, Start: "(" + score, Stop: "-inf", ByScore: true, Rev: true, Count: int64(n)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	cursor := timelineMember(position.ID)
	members := make([]string, 0, n)
	// 同一毫秒里只要 ID 比游标小的；成员都补了零，直接比较字符串就是比较 ID 大小。
	for _, member := range same.Val() {
		if member < cursor {
			members = append(members, member)
		}
	}
	members = append(members, older.Val()...)
	if len(members) > n {
		members = members[:n]
	}
	return members, nil
}

// Rebuild 用 MySQL 最新的视频重建时间线。newest 按时间从新到旧，最多 Size+1 条：
// 多出来的那 1 条只用来判断窗口外还有没有更早的视频，本身不放进时间线。
func (t *RedisTimeline) Rebuild(ctx context.Context, newest []FeedItem) error {
	// TTL 为 0 时 EXPIRE 会立刻删掉 key，而时间线靠过期兜底，所以配置不对就不写。
	if t.Size <= 0 || t.TTL <= 0 {
		return errors.New("时间线的窗口大小和过期时间必须大于 0")
	}
	bottom := sentinelEnd
	if len(newest) > t.Size {
		newest, bottom = newest[:t.Size], sentinelMore
	}
	members := make([]redis.Z, 0, len(newest)+1)
	videos := make(map[string]any, len(newest))
	for _, item := range newest {
		data, err := json.Marshal(timelineVideo{
			ID: item.ID, AuthorID: item.AuthorID, Title: item.Title,
			PlayURL: item.PlayURL, CoverURL: item.CoverURL, CreatedAt: item.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("编码时间线视频失败: %w", err)
		}
		member := timelineMember(item.ID)
		members = append(members, redis.Z{Score: float64(item.CreatedAt.UnixMilli()), Member: member})
		videos[member] = data
	}
	members = append(members, redis.Z{Score: 0, Member: bottom})
	// 删旧、写新、设过期放进同一个 MULTI：别人读到的要么是完整的旧时间线，要么是完整的新时间线。
	_, err := t.Client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, timelineIDsKey, timelineVideosKey)
		pipe.ZAdd(ctx, timelineIDsKey, members...)
		pipe.Expire(ctx, timelineIDsKey, t.TTL)
		if len(videos) > 0 {
			pipe.HSet(ctx, timelineVideosKey, videos)
			pipe.Expire(ctx, timelineVideosKey, t.TTL)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("重建时间线失败: %w", err)
	}
	return nil
}

// Invalidate 删掉整条时间线；下一次读取时会从 MySQL 重建。
func (t *RedisTimeline) Invalidate(ctx context.Context) error {
	if err := t.Client.Del(ctx, timelineIDsKey, timelineVideosKey).Err(); err != nil {
		return fmt.Errorf("删除时间线失败: %w", err)
	}
	return nil
}
