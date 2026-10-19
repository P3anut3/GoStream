package video

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gostream/internal/auth"
)

func TestLikeValidationAndDatabaseFailure(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&uploadTestStore{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	h := server.New()
	handler := &Handler{DB: db, LikeService: NewLikeService(&SQLLikeRepository{DB: db})}
	h.PUT("/videos/:id/like", auth.JWTAuth(secret), handler.Like)
	h.GET("/videos/:id/like", auth.JWTAuth(secret), handler.GetLikeStatus)
	h.DELETE("/videos/:id/like", auth.JWTAuth(secret), handler.UnLike)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			for _, tc := range []struct {
				name, id, token string
				want            int
			}{
				{"no_token", "1", "", 401},
				{"invalid_id", "abc", token, 400},
				{"zero_id", "0", token, 400},
				{"negative_id", "-1", token, 400},
				{"database_failure", "1", token, 500},
			} {
				t.Run(tc.name, func(t *testing.T) {
					headers := []ut.Header{}
					if tc.token != "" {
						headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + tc.token})
					}
					resp := ut.PerformRequest(h.Engine, method, "/videos/"+tc.id+"/like", nil, headers...).Result()
					if resp.StatusCode() != tc.want {
						t.Errorf("expected %d, got %d", tc.want, resp.StatusCode())
					}
					if tc.name == "database_failure" {
						var failure struct {
							Error string `json:"error"`
						}
						wantMessage := "点赞失败"
						if method == http.MethodDelete {
							wantMessage = "取消点赞失败"
						}
						if method == http.MethodGet {
							wantMessage = "查询点赞状态失败"
						}
						if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error != wantMessage {
							t.Fatal("valid request must reach the database rather than an uninitialized service")
						}
					}
				})
			}
		})
	}
}
