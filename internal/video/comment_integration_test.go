package video

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Reuses TestDeleteIntegration's two accounts and video; its cleanup owns all fixtures.
func testCreateComments(t *testing.T, db *sql.DB,
	request func(string, string, string, interface{}) (int, []byte),
	videoID, ownerID, otherID uint64, ownerToken, otherToken string,
) {
	t.Helper()
	path := "/videos/" + strconv.FormatUint(videoID, 10) + "/comments"
	seen := make(map[uint64]bool)
	for _, tc := range []struct {
		name, target, token, content, stored string
		uid                                  uint64
		want                                 int
		spoof                                bool
	}{
		{"comment_normal", path, ownerToken, "  拍得不错！  ", "拍得不错！", ownerID, 201, false},
		{"comment_repeated_content", path, ownerToken, "拍得不错！", "拍得不错！", ownerID, 201, false},
		{"comment_other_user", path, otherToken, "我也喜欢", "我也喜欢", otherID, 201, false},
		{"comment_500_unicode_characters", path, ownerToken, strings.Repeat("评", 500), strings.Repeat("评", 500), ownerID, 201, false},
		{"comment_501_unicode_characters", path, ownerToken, strings.Repeat("评", 501), "", ownerID, 400, false},
		{"comment_empty", path, ownerToken, "", "", ownerID, 400, false},
		{"comment_whitespace", path, ownerToken, " \t\n　", "", ownerID, 400, false},
		{"comment_missing_video", "/videos/18446744073709551615/comments", ownerToken, "hello", "", ownerID, 404, false},
		{"comment_without_token", path, "", "hello", "", ownerID, 401, false},
		{"comment_invalid_token", path, "invalid", "hello", "", ownerID, 401, false},
		{"comment_ignores_forged_user_id", path, ownerToken, "身份来自 JWT", "身份来自 JWT", ownerID, 201, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before int
			if err := db.QueryRow("SELECT COUNT(*) FROM video_comments WHERE video_id = ?", videoID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			payload := map[string]interface{}{"content": tc.content}
			if tc.spoof {
				payload["user_id"] = otherID
			}
			status, data := request(http.MethodPost, tc.target, tc.token, payload)
			if status != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, status)
			}
			if status == 201 {
				var comment struct {
					ID      uint64 `json:"id"`
					VideoID uint64 `json:"video_id"`
					UserID  uint64 `json:"user_id"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(data, &comment); err != nil {
					t.Fatal(err)
				}
				if comment.ID == 0 || seen[comment.ID] || comment.VideoID != videoID || comment.UserID != tc.uid || comment.Content != tc.stored {
					t.Fatal("incorrect comment ID, identity or content")
				}
				seen[comment.ID] = true
				var uid, vid uint64
				var content string
				if err := db.QueryRow("SELECT user_id, video_id, content FROM video_comments WHERE id = ?", comment.ID).Scan(&uid, &vid, &content); err != nil || uid != tc.uid || vid != videoID || content != tc.stored {
					t.Fatal("response must match the newly inserted database row")
				}
				before++
			}
			var after int
			if err := db.QueryRow("SELECT COUNT(*) FROM video_comments WHERE video_id = ?", videoID).Scan(&after); err != nil || after != before {
				t.Error("incorrect comment count after request")
			}
		})
	}
}

func testListComments(t *testing.T, db *sql.DB,
	request func(string, string, string, interface{}) (int, []byte),
	videoID, otherVideoID uint64, token string,
) {
	t.Helper()
	path := "/videos/" + strconv.FormatUint(videoID, 10) + "/comments"
	otherPath := "/videos/" + strconv.FormatUint(otherVideoID, 10) + "/comments"
	type response struct {
		VideoID  uint64    `json:"video_id"`
		Page     int       `json:"page"`
		Size     int       `json:"page_size"`
		Comments []Comment `json:"comments"`
	}
	read := func(t *testing.T, path string) response {
		t.Helper()
		status, body := request(http.MethodGet, path, "", nil)
		if status != 200 {
			t.Fatalf("anonymous comment listing expected 200, got %d", status)
		}
		var list response
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if list.Comments == nil {
			t.Fatal("comments must be an array, including when empty")
		}
		return list
	}
	t.Run("comments_existing_video_empty_array", func(t *testing.T) {
		list := read(t, otherPath)
		if list.VideoID != otherVideoID || len(list.Comments) != 0 {
			t.Fatal("new video must have an empty comment list")
		}
	})
	// Add a comment to the second video to catch missing WHERE video_id filtering.
	status, _ := request(http.MethodPost, otherPath, token, map[string]string{"content": "only the other video"})
	if status != 201 {
		t.Fatal("cannot create comment isolation fixture")
	}
	rows, err := db.Query("SELECT id, video_id, user_id, content, created_at FROM video_comments WHERE video_id = ?", videoID)
	if err != nil {
		t.Fatal(err)
	}
	var expected []Comment
	for rows.Next() {
		var item Comment
		if err := rows.Scan(&item.ID, &item.VideoID, &item.UserID, &item.Content, &item.CreatedAt); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		expected = append(expected, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(expected) != 5 {
		t.Fatalf("expected 5 known comments, got %d", len(expected))
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].ID > expected[j].ID })
	for _, tc := range []struct {
		name, query            string
		page, size, start, end int
	}{
		{"comments_default_pagination", "", 1, 10, 0, 5},
		{"comments_first_page", "?page=1&page_size=2", 1, 2, 0, 2},
		{"comments_second_page", "?page=2&page_size=2", 2, 2, 2, 4},
		{"comments_last_partial_page", "?page=3&page_size=2", 3, 2, 4, 5},
		{"comments_beyond_last_page", "?page=4&page_size=2", 4, 2, 5, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := read(t, path+tc.query)
			if list.VideoID != videoID || list.Page != tc.page || list.Size != tc.size || len(list.Comments) != tc.end-tc.start {
				t.Fatal("incorrect pagination metadata or count")
			}
			for i, item := range list.Comments {
				want := expected[tc.start+i]
				if item.ID != want.ID || item.VideoID != videoID || item.UserID != want.UserID || item.Content != want.Content || !item.CreatedAt.Equal(want.CreatedAt) {
					t.Fatal("incorrect order, video scope or comment fields")
				}
			}
		})
	}
	t.Run("comments_other_video_isolation", func(t *testing.T) {
		list := read(t, otherPath)
		if len(list.Comments) != 1 || list.Comments[0].VideoID != otherVideoID || list.Comments[0].Content != "only the other video" {
			t.Fatal("comments from different videos must not mix")
		}
	})
	t.Run("comments_missing_video", func(t *testing.T) {
		status, _ := request(http.MethodGet, "/videos/18446744073709551615/comments", "", nil)
		if status != 404 {
			t.Errorf("expected 404 for nonexistent video, got %d", status)
		}
	})
}

func testDeleteComments(t *testing.T, db *sql.DB,
	request func(string, string, string, interface{}) (int, []byte),
	videoA, videoB uint64, tokenA, tokenB string,
) {
	t.Helper()
	var before int
	if err := db.QueryRow("SELECT COUNT(*) FROM video_comments WHERE video_id IN (?, ?)", videoA, videoB).Scan(&before); err != nil {
		t.Fatal(err)
	}
	create := func(videoID uint64, token string) uint64 {
		t.Helper()
		status, data := request(http.MethodPost, "/videos/"+strconv.FormatUint(videoID, 10)+"/comments", token, map[string]string{"content": "delete permission fixture"})
		if status != 201 {
			t.Fatalf("fixture comment expected 201, got %d", status)
		}
		var comment struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(data, &comment); err != nil || comment.ID == 0 {
			t.Fatal("invalid fixture comment ID")
		}
		return comment.ID
	}
	// A comments on B's video; B comments on A's video.
	commentA, commentB := create(videoB, tokenA), create(videoA, tokenB)
	pathA, pathB := "/comments/"+strconv.FormatUint(commentA, 10), "/comments/"+strconv.FormatUint(commentB, 10)
	for _, tc := range []struct {
		name, path, token            string
		want, remainingA, remainingB int
	}{
		{"delete_comment_without_token", pathA, "", 401, 1, 1},
		{"delete_comment_invalid_token", pathA, "invalid", 401, 1, 1},
		{"delete_comment_invalid_id", "/comments/abc", tokenA, 400, 1, 1},
		{"delete_comment_zero_id", "/comments/0", tokenA, 400, 1, 1},
		{"delete_comment_negative_id", "/comments/-1", tokenA, 400, 1, 1},
		{"delete_comment_missing", "/comments/18446744073709551615", tokenA, 404, 1, 1},
		{"video_owner_B_cannot_delete_A_comment", pathA, tokenB, 404, 1, 1},
		{"video_owner_A_cannot_delete_B_comment", pathB, tokenA, 404, 1, 1},
		{"comment_author_A_can_delete", pathA, tokenA, 204, 0, 1},
		{"delete_comment_repeat", pathA, tokenA, 404, 0, 1},
		{"comment_author_B_can_delete", pathB, tokenB, 204, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := request(http.MethodDelete, tc.path, tc.token, nil)
			if status != tc.want {
				t.Errorf("expected %d, got %d", tc.want, status)
			}
			if tc.want == 204 && len(body) != 0 {
				t.Error("204 must have no body")
			}
			for _, record := range []struct {
				id   uint64
				want int
			}{{commentA, tc.remainingA}, {commentB, tc.remainingB}} {
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM video_comments WHERE id = ?", record.id).Scan(&count); err != nil || count != record.want {
					t.Error("delete operation changed an unexpected comment or failed to preserve one")
				}
			}
		})
	}
	t.Run("delete_comment_preserves_other_comments", func(t *testing.T) {
		var after int
		if err := db.QueryRow("SELECT COUNT(*) FROM video_comments WHERE video_id IN (?, ?)", videoA, videoB).Scan(&after); err != nil || after != before {
			t.Error("existing comments must survive deleting the target comments")
		}
	})
}
