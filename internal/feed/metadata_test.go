package feed

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gostream/internal/database"
)

type metadataRepository struct {
	feedRepositoryFunc
	load func(context.Context, []uint64, []uint64, uint64) (Metadata, error)
}

func (r metadataRepository) LoadMetadata(c context.Context, v, a []uint64, u uint64) (Metadata, error) {
	return r.load(c, v, a, u)
}

func TestServiceBatchMetadata(t *testing.T) {
	for _, viewer := range []uint64{0, 7, 8} {
		ctx := context.WithValue(context.Background(), feedContextKey{}, "batch")
		calls := 0
		repo := metadataRepository{
			feedRepositoryFunc: func(context.Context, *Position, int) ([]FeedItem, error) {
				return []FeedItem{{ID: 30, AuthorID: 2, CreatedAt: time.Now()}, {ID: 29, AuthorID: 2, CreatedAt: time.Now()}, {ID: 28, AuthorID: 3, CreatedAt: time.Now()}}, nil
			},
			load: func(got context.Context, ids, authors []uint64, user uint64) (Metadata, error) {
				calls++
				if got != ctx || !reflect.DeepEqual(ids, []uint64{30, 29}) || !reflect.DeepEqual(authors, []uint64{2}) || user != viewer {
					t.Fatal("incorrect batch scope or trusted viewer")
				}
				return Metadata{Authors: map[uint64]string{2: "作者"}, Likes: map[uint64]LikeSummary{30: {LikeCount: 2, Liked: viewer == 7 || viewer == 0}}}, nil
			},
		}
		result, err := NewService(repo, testCursorCodec(t)).ListLatestForViewer(ctx, 2, "", viewer)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || result.Videos[0].ID != 30 || result.Videos[1].ID != 29 || result.Videos[0].AuthorName != "作者" || result.Videos[0].LikeCount != 2 || result.Videos[0].Liked != (viewer == 7) || result.Videos[1].LikeCount != 0 || result.Videos[1].Liked {
			t.Fatalf("incorrect enrichment: %+v", result)
		}
	}
}
func TestServiceMetadataErrors(t *testing.T) {
	failure := errors.New("metadata unavailable")
	r := metadataRepository{feedRepositoryFunc: func(context.Context, *Position, int) ([]FeedItem, error) { return []FeedItem{{ID: 1}}, nil }, load: func(context.Context, []uint64, []uint64, uint64) (Metadata, error) { return Metadata{}, failure }}
	result, err := NewService(r, testCursorCodec(t)).ListLatest(context.Background(), 10, "")
	if !errors.Is(err, failure) || len(result.Videos) != 0 {
		t.Fatal("metadata failure must propagate")
	}
	r.feedRepositoryFunc = func(context.Context, *Position, int) ([]FeedItem, error) { return nil, nil }
	r.load = func(context.Context, []uint64, []uint64, uint64) (Metadata, error) {
		t.Fatal("empty page must not enrich")
		return Metadata{}, nil
	}
	if _, err := NewService(r, testCursorCodec(t)).ListLatest(context.Background(), 10, ""); err != nil {
		t.Fatal(err)
	}
}
func TestRepositoryBatchMetadata(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		failure := errors.New("storage failure")
		calls := 0
		ctx := context.WithValue(context.Background(), feedContextKey{}, "batch")
		pool := sql.OpenDB(feedConnector{query: func(got context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
			calls++
			if got != ctx {
				t.Fatal("context lost")
			}
			if calls == failAt {
				return nil, failure
			}
			if calls == 1 {
				if !strings.Contains(q, "users") || strings.Contains(q, "password") || len(args) != 1 || args[0].Value != int64(2) {
					t.Fatalf("unexpected author query: %s", q)
				}
				return &feedRows{columns: []string{"id", "username"}, values: [][]driver.Value{{int64(2), "作者"}}}, nil
			}
			if calls != 2 || !strings.Contains(q, "GROUP BY video_id") || len(args) != 3 || args[0].Value != int64(7) || args[1].Value != int64(30) || args[2].Value != int64(29) {
				t.Fatalf("unexpected like batch: %s", q)
			}
			return &feedRows{columns: []string{"video_id", "like_count", "liked"}, values: [][]driver.Value{{int64(30), int64(2), int64(1)}}}, nil
		}})
		m, err := (&GORMRepository{DB: database.WrapSQL(pool)}).LoadMetadata(ctx, []uint64{30, 29}, []uint64{2}, 7)
		if failAt > 0 {
			if !errors.Is(err, failure) || calls != failAt {
				t.Fatalf("failure not preserved: %v", err)
			}
		} else if err != nil || calls != 2 || m.Authors[2] != "作者" || m.Likes[30].LikeCount != 2 || !m.Likes[30].Liked {
			t.Fatalf("incorrect metadata: %+v %v", m, err)
		}
		pool.Close()
	}
	if _, err := (&GORMRepository{}).LoadMetadata(context.Background(), nil, nil, 0); err != nil {
		t.Fatal(err)
	}
}
