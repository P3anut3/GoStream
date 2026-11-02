package feed

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type feedRepositoryFunc func(context.Context, *Position, int) ([]FeedItem, error)

func (f feedRepositoryFunc) ListLatest(ctx context.Context, position *Position, limit int) ([]FeedItem, error) {
	return f(ctx, position, limit)
}

func (f feedRepositoryFunc) LoadMetadata(context.Context, []uint64, []uint64, uint64) (Metadata, error) {
	return Metadata{}, nil
}

func TestServicePagination(t *testing.T) {
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		ids  []uint64
		want []uint64
		more bool
	}{
		{name: "nil_result_is_empty_array", want: []uint64{}},
		{name: "fewer_than_limit", ids: []uint64{30}, want: []uint64{30}},
		{name: "exactly_limit_is_last_batch", ids: []uint64{30, 29}, want: []uint64{30, 29}},
		{name: "extra_row_is_not_returned_or_skipped", ids: []uint64{30, 29, 28}, want: []uint64{30, 29}, more: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), feedContextKey{}, "service-request")
			codec := testCursorCodec(t)
			repo := feedRepositoryFunc(func(got context.Context, position *Position, limit int) ([]FeedItem, error) {
				if got != ctx || position != nil || limit != 3 {
					t.Fatal("first request must forward context and request three rows without a boundary")
				}
				var items []FeedItem
				for _, id := range tc.ids {
					items = append(items, FeedItem{ID: id, CreatedAt: stamp})
				}
				return items, nil
			})
			response, err := NewService(repo, codec).ListLatest(ctx, 2, "")
			if err != nil {
				t.Fatal(err)
			}
			gotIDs := make([]uint64, len(response.Videos))
			for i, item := range response.Videos {
				gotIDs[i] = item.ID
			}
			if !reflect.DeepEqual(gotIDs, tc.want) || response.HasMore != tc.more {
				t.Fatalf("unexpected page: %+v", response)
			}
			if !tc.more {
				if response.NextCursor != "" {
					t.Error("last batch must not contain a next cursor")
				}
			} else {
				position, err := codec.Decode(response.NextCursor)
				if err != nil || position == nil || position.ID != 29 || !position.CreatedAt.Equal(stamp) {
					t.Fatalf("cursor must reference returned item 29, not extra item 28: %+v, %v", position, err)
				}
			}
			if len(tc.want) == 0 {
				data, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != `{"videos":[],"next_cursor":"","has_more":false}` {
					t.Fatalf("incorrect empty response: %s", data)
				}
			}
		})
	}
}

func TestServiceLimitRules(t *testing.T) {
	for _, tc := range []struct{ limit, wantQueryLimit int }{{0, 11}, {1, 2}, {10, 11}, {50, 51}, {100, 51}} {
		calls := 0
		repo := feedRepositoryFunc(func(_ context.Context, _ *Position, intLimit int) ([]FeedItem, error) {
			calls++
			if intLimit != tc.wantQueryLimit {
				t.Errorf("limit %d: queried %d, want %d", tc.limit, intLimit, tc.wantQueryLimit)
			}
			return nil, nil
		})
		_, err := NewService(repo, testCursorCodec(t)).ListLatest(context.Background(), tc.limit, "")
		if err != nil || calls != 1 {
			t.Fatalf("valid limit rejected: %v", err)
		}
	}
}

func TestServiceRejectsInvalidInputBeforeQuery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		token string
		want  error
	}{
		{"negative_limit", -1, "", ErrInvalidLimit},
		{"invalid_cursor", 10, "invalid", ErrInvalidCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := feedRepositoryFunc(func(context.Context, *Position, int) ([]FeedItem, error) {
				t.Fatal("invalid input must not query database")
				return nil, nil
			})
			_, err := NewService(repo, testCursorCodec(t)).ListLatest(context.Background(), tc.limit, tc.token)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestServiceContinuesFromReturnedCursor(t *testing.T) {
	codec := testCursorCodec(t)
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	calls := 0
	repo := feedRepositoryFunc(func(_ context.Context, position *Position, limit int) ([]FeedItem, error) {
		calls++
		if limit != 3 {
			t.Fatal("expected lookahead row")
		}
		if calls == 1 {
			if position != nil {
				t.Fatal("first batch must have no boundary")
			}
			return []FeedItem{{ID: 30, CreatedAt: stamp}, {ID: 29, CreatedAt: stamp}, {ID: 28, CreatedAt: stamp}}, nil
		}
		if position == nil || position.ID != 29 || !position.CreatedAt.Equal(stamp) {
			t.Fatal("returned cursor did not reach repository as the correct position")
		}
		return []FeedItem{{ID: 28, CreatedAt: stamp}}, nil
	})
	service := NewService(repo, codec)
	first, err := service.ListLatest(context.Background(), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ListLatest(context.Background(), 2, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(second.Videos) != 1 || second.Videos[0].ID != 28 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("lookahead item must appear in next batch: %+v", second)
	}
}

func TestServicePreservesRepositoryErrors(t *testing.T) {
	for _, failure := range []error{errors.New("database unavailable"), context.Canceled} {
		repo := feedRepositoryFunc(func(context.Context, *Position, int) ([]FeedItem, error) { return nil, failure })
		response, err := NewService(repo, testCursorCodec(t)).ListLatest(context.Background(), 10, "")
		if !errors.Is(err, failure) || response.HasMore || response.NextCursor != "" || len(response.Videos) != 0 {
			t.Fatalf("failed query returned success data: %+v, %v", response, err)
		}
	}
}
