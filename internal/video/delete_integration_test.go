package video

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gostream/internal/auth"
	"gostream/internal/database"
	"gostream/internal/user"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/joho/godotenv"
)

// TestDeleteIntegration uses the configured MySQL database only when explicitly enabled.
// Requests run in process without starting a listener. All fixtures are cleaned up.
func TestDeleteIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 to run against local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read project .env")
	}
	db, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured MySQL")
	}
	t.Cleanup(func() { db.Close() })
	secret := []byte(config["JWT_SECRET"])
	if len(secret) < 32 {
		t.Fatal("JWT_SECRET must contain at least 32 bytes")
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("cannot generate fixture identifier")
	}
	prefix := "deltest_" + hex.EncodeToString(random[:])
	h := server.New()
	users := &user.UserHandler{DB: db, JWTSecret: secret}
	uploadDir := t.TempDir()
	videos := &Handler{DB: db, UploadDir: uploadDir, CoverDir: t.TempDir(), CommentService: NewCommentService(&SQLCommentRepository{DB: db}), LikeService: NewLikeService(&SQLLikeRepository{DB: db})}
	h.POST("/users", users.CreateUser)
	h.POST("/login", users.Login)
	h.POST("/videos", auth.JWTAuth(secret), videos.Publish)
	h.POST("/uploads/videos", auth.JWTAuth(secret), videos.Upload)
	h.POST("/uploads/covers", auth.JWTAuth(secret), videos.UploadCover)
	h.PUT("/videos/:id/like", auth.JWTAuth(secret), videos.Like)
	h.DELETE("/videos/:id/like", auth.JWTAuth(secret), videos.UnLike)
	h.GET("/videos/:id/like", auth.JWTAuth(secret), videos.GetLikeStatus)
	h.POST("/videos/:id/comments", auth.JWTAuth(secret), videos.CreateComment)
	h.GET("/videos/:id/comments", videos.ListComments)
	h.DELETE("/comments/:id", auth.JWTAuth(secret), videos.DeleteComment)
	h.GET("/videos/:id", videos.GetByID)
	h.GET("/videos", videos.List)
	h.GET("/me/videos", auth.JWTAuth(secret), videos.ListMine)
	h.DELETE("/videos/:id", auth.JWTAuth(secret), videos.Delete)

	request := func(method, path, token string, payload interface{}) (int, []byte) {
		t.Helper()
		var body *ut.Body
		headers := []ut.Header{}
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal("cannot encode test request")
			}
			body = &ut.Body{Body: bytes.NewReader(data), Len: len(data)}
			headers = append(headers, ut.Header{Key: "Content-Type", Value: "application/json"})
		}
		if token != "" {
			headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + token})
		}
		resp := ut.PerformRequest(h.Engine, method, path, body, headers...).Result()
		return resp.StatusCode(), append([]byte(nil), resp.Body()...)
	}
	createAccount := func(suffix string) (uint64, string) {
		t.Helper()
		name := prefix + suffix
		payload := map[string]string{"username": name, "password": "integration-test-password"}
		status, data := request(http.MethodPost, "/users", "", payload)
		if status != http.StatusCreated {
			t.Fatalf("fixture registration: expected 201, got %d", status)
		}
		// Cleanup matches this run's exact unique username; videos go first for the FK.
		t.Cleanup(func() {
			if _, err := db.Exec("DELETE FROM videos WHERE author_id IN (SELECT id FROM users WHERE username = ?)", name); err != nil {
				t.Error("failed to clean fixture videos")
			}
			if _, err := db.Exec("DELETE FROM uploads WHERE user_id IN (SELECT id FROM users WHERE username = ?)", name); err != nil {
				t.Error("failed to clean fixture upload records")
			}
			if _, err := db.Exec("DELETE FROM video_likes WHERE user_id IN (SELECT id FROM users WHERE username = ?)", name); err != nil {
				t.Error("failed to clean fixture likes")
			}
			if _, err := db.Exec("DELETE FROM video_comments WHERE user_id IN (SELECT id FROM users WHERE username = ?)", name); err != nil {
				t.Error("failed to clean fixture comments")
			}
			if _, err := db.Exec("DELETE FROM users WHERE username = ?", name); err != nil {
				t.Error("failed to clean fixture account")
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", name).Scan(&count); err != nil || count != 0 {
				t.Error("fixture cleanup verification failed")
			}
		})
		var account struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(data, &account); err != nil || account.ID == 0 {
			t.Fatal("registration did not return a valid user ID")
		}
		status, data = request(http.MethodPost, "/login", "", payload)
		if status != http.StatusOK {
			t.Fatalf("fixture login: expected 200, got %d", status)
		}
		var login struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(data, &login); err != nil || login.Token == "" {
			t.Fatal("login did not return a token")
		}
		return account.ID, login.Token
	}
	ownerID, ownerToken := createAccount("a")
	otherID, otherToken := createAccount("b")
	var ownerCoverName, ownerCoverURL string
	t.Run("cover_upload_records_owner_and_type", func(t *testing.T) {
		var imageBytes, body bytes.Buffer
		if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
			t.Fatal(err)
		}
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "cover.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(imageBytes.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		resp := ut.PerformRequest(h.Engine, http.MethodPost, "/uploads/covers",
			&ut.Body{Body: bytes.NewReader(body.Bytes()), Len: body.Len()},
			ut.Header{Key: "Content-Type", Value: writer.FormDataContentType()},
			ut.Header{Key: "Authorization", Value: "Bearer " + ownerToken}).Result()
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("cover upload expected 200, got %d", resp.StatusCode())
		}
		var cover struct {
			SavedName string `json:"saved_name"`
			CoverURL  string `json:"cover_url"`
		}
		if err := json.Unmarshal(resp.Body(), &cover); err != nil || cover.SavedName == "" || cover.CoverURL != "/media/covers/"+cover.SavedName {
			t.Fatal("invalid cover response")
		}
		ownerCoverName, ownerCoverURL = cover.SavedName, cover.CoverURL
		var uid uint64
		var kind string
		if err := db.QueryRow("SELECT user_id, file_type FROM uploads WHERE saved_name = ?", cover.SavedName).Scan(&uid, &kind); err != nil || uid != ownerID || kind != "cover" {
			t.Fatal("cover owner or type not recorded correctly")
		}
		saved, err := os.ReadFile(filepath.Join(videos.CoverDir, cover.SavedName))
		if err != nil || !bytes.Equal(saved, imageBytes.Bytes()) {
			t.Fatal("cover file bytes differ from upload")
		}
	})
	if ownerCoverName == "" {
		t.Fatal("cover fixture was not created")
	}
	ownerCoverBytes, err := os.ReadFile(filepath.Join(videos.CoverDir, ownerCoverName))
	if err != nil {
		t.Fatal(err)
	}
	// A second user's cover fixture isolates cover ownership from video ownership.
	otherCoverName := prefix + "_other.png"
	if err := os.WriteFile(filepath.Join(videos.CoverDir, otherCoverName), ownerCoverBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO uploads (user_id, saved_name, file_type) VALUES (?, ?, ?)", otherID, otherCoverName, "cover"); err != nil {
		t.Fatal(err)
	}
	// Upload a real fixture through the handler so publication uses a recorded owner.
	content := make([]byte, 1024)
	copy(content, []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'})
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	part, err := writer.CreateFormFile("file", "test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	resp := ut.PerformRequest(h.Engine, http.MethodPost, "/uploads/videos",
		&ut.Body{Body: bytes.NewReader(multipartBody.Bytes()), Len: multipartBody.Len()},
		ut.Header{Key: "Content-Type", Value: writer.FormDataContentType()},
		ut.Header{Key: "Authorization", Value: "Bearer " + ownerToken}).Result()
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("fixture upload: expected 200, got %d", resp.StatusCode())
	}
	var upload struct {
		SavedName string `json:"saved_name"`
		PlayURL   string `json:"play_url"`
	}
	if err := json.Unmarshal(resp.Body(), &upload); err != nil || upload.SavedName == "" {
		t.Fatal("upload did not return a saved filename")
	}
	var recordedOwner uint64
	var fileType string
	if err := db.QueryRow("SELECT user_id, file_type FROM uploads WHERE saved_name = ?", upload.SavedName).Scan(&recordedOwner, &fileType); err != nil || recordedOwner != ownerID || fileType != "video" {
		t.Fatal("upload record must match the authenticated owner and video type")
	}
	for _, tc := range []struct {
		name, token, url string
		want             int
	}{
		{"publish_without_token", "", upload.PlayURL, 401},
		{"other_user_cannot_publish", otherToken, upload.PlayURL, 403},
		{"unrecorded_file", ownerToken, "/media/videos/" + prefix + ".mp4", 403},
		{"external_url", ownerToken, "https://example.com/test.mp4", 400},
		{"path_traversal", ownerToken, "/media/videos/../" + upload.SavedName, 400},
		{"encoded_path", ownerToken, "/media/videos/%2e%2e%2ftest.mp4", 400},
		{"query_suffix", ownerToken, upload.PlayURL + "?test=1", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := request(http.MethodPost, "/videos", tc.token, map[string]string{
				"title": prefix, "play_url": tc.url, "cover_url": "/cover.svg",
			})
			if status != tc.want {
				t.Errorf("expected %d, got %d", tc.want, status)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE title = ?", prefix).Scan(&count); err != nil || count != 0 {
				t.Error("rejected publication must not create a video record")
			}
			data, err := os.ReadFile(filepath.Join(uploadDir, upload.SavedName))
			if err != nil || !bytes.Equal(data, content) {
				t.Error("rejected publication must preserve the owner's file")
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM uploads WHERE saved_name = ? AND user_id = ?", upload.SavedName, ownerID).Scan(&count); err != nil || count != 1 {
				t.Error("rejected publication must preserve the upload record")
			}
		})
	}
	t.Run("cover_cannot_be_published_as_video", func(t *testing.T) {
		if _, err := db.Exec("UPDATE uploads SET file_type = 'cover' WHERE saved_name = ? AND user_id = ?", upload.SavedName, ownerID); err != nil {
			t.Fatal("cannot set fixture type")
		}
		defer func() {
			if _, err := db.Exec("UPDATE uploads SET file_type = 'video' WHERE saved_name = ? AND user_id = ?", upload.SavedName, ownerID); err != nil {
				t.Error("cannot restore fixture type")
			}
		}()
		status, _ := request(http.MethodPost, "/videos", ownerToken, map[string]string{
			"title": prefix, "play_url": upload.PlayURL, "cover_url": "/cover.svg",
		})
		if status != http.StatusForbidden {
			t.Errorf("expected 403, got %d", status)
		}
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE title = ?", prefix).Scan(&count); err != nil || count != 0 {
			t.Error("wrong file type must not create a video record")
		}
		data, err := os.ReadFile(filepath.Join(uploadDir, upload.SavedName))
		if err != nil || !bytes.Equal(data, content) {
			t.Error("wrong file type must not delete the file")
		}
	})
	for _, tc := range []struct {
		name, coverURL string
		want           int
	}{
		{"other_users_cover", "/media/covers/" + otherCoverName, 403},
		{"missing_cover", "/media/covers/" + prefix + "_missing.png", 403},
		{"video_used_as_cover", "/media/covers/" + upload.SavedName, 403},
		{"external_cover", "https://example.com/cover.jpg", 400},
		{"cover_traversal", "/media/covers/../" + ownerCoverName, 400},
		{"cover_encoded_path", "/media/covers/%2e%2e/cover.png", 400},
		{"cover_query", ownerCoverURL + "?x=1", 400},
		{"cover_fragment", ownerCoverURL + "#x", 400},
		{"empty_cover_filename", "/media/covers/", 400},
		{"default_cover_alias", "/cover.svg?x=1", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := request(http.MethodPost, "/videos", ownerToken, map[string]string{
				"title": prefix, "play_url": upload.PlayURL, "cover_url": tc.coverURL,
			})
			if status != tc.want {
				t.Errorf("expected %d, got %d", tc.want, status)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE title = ?", prefix).Scan(&count); err != nil || count != 0 {
				t.Error("rejected cover must not create a video record")
			}
			for _, name := range []string{ownerCoverName, otherCoverName} {
				data, err := os.ReadFile(filepath.Join(videos.CoverDir, name))
				if err != nil || !bytes.Equal(data, ownerCoverBytes) {
					t.Error("rejected cover must preserve uploaded files")
				}
				if err := db.QueryRow("SELECT COUNT(*) FROM uploads WHERE saved_name = ?", name).Scan(&count); err != nil || count != 1 {
					t.Error("rejected cover must preserve upload records")
				}
			}
		})
	}
	t.Run("owner_can_publish_with_uploaded_cover", func(t *testing.T) {
		status, data := request(http.MethodPost, "/videos", ownerToken, map[string]string{
			"title": prefix, "play_url": upload.PlayURL, "cover_url": ownerCoverURL,
		})
		if status != http.StatusCreated {
			t.Fatalf("expected 201, got %d", status)
		}
		var published struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(data, &published); err != nil || published.ID == 0 {
			t.Fatal("invalid publication response")
		}
		var storedCover string
		if err := db.QueryRow("SELECT cover_url FROM videos WHERE id = ? AND author_id = ?", published.ID, ownerID).Scan(&storedCover); err != nil || storedCover != ownerCoverURL {
			t.Error("published cover URL was not stored correctly")
		}
		if _, err := db.Exec("DELETE FROM videos WHERE id = ? AND author_id = ?", published.ID, ownerID); err != nil {
			t.Error("cannot clean cover publication fixture")
		}
	})
	status, data := request(http.MethodPost, "/videos", ownerToken, map[string]string{
		"title": prefix, "play_url": upload.PlayURL, "cover_url": "/cover.svg",
	})
	if status != http.StatusCreated {
		t.Fatalf("fixture video publication: expected 201, got %d", status)
	}
	var published struct {
		ID       uint64 `json:"id"`
		AuthorID uint64 `json:"author_id"`
	}
	if err := json.Unmarshal(data, &published); err != nil || published.ID == 0 || published.AuthorID != ownerID {
		t.Fatal("publication did not return expected ownership")
	}
	path := "/videos/" + strconv.FormatUint(published.ID, 10)
	testCreateComments(t, db, request, published.ID, ownerID, otherID, ownerToken, otherToken)
	// Each account owns a separate uploaded video, so filtering is observable.
	otherResp := ut.PerformRequest(h.Engine, http.MethodPost, "/uploads/videos",
		&ut.Body{Body: bytes.NewReader(multipartBody.Bytes()), Len: multipartBody.Len()},
		ut.Header{Key: "Content-Type", Value: writer.FormDataContentType()},
		ut.Header{Key: "Authorization", Value: "Bearer " + otherToken}).Result()
	var otherUpload struct {
		PlayURL string `json:"play_url"`
	}
	if otherResp.StatusCode() != 200 || json.Unmarshal(otherResp.Body(), &otherUpload) != nil || otherUpload.PlayURL == "" {
		t.Fatal("other user's upload failed")
	}
	status, _ = request(http.MethodPost, "/videos", otherToken, map[string]string{
		"title": prefix + "_other", "play_url": otherUpload.PlayURL, "cover_url": "/cover.svg",
	})
	if status != 201 {
		t.Fatalf("other publication expected 201, got %d", status)
	}
	var commentsOtherVideoID uint64
	if err := db.QueryRow("SELECT id FROM videos WHERE author_id = ? AND title = ?", otherID, prefix+"_other").Scan(&commentsOtherVideoID); err != nil {
		t.Fatal(err)
	}
	testListComments(t, db, request, published.ID, commentsOtherVideoID, otherToken)
	testDeleteComments(t, db, request, published.ID, commentsOtherVideoID, ownerToken, otherToken)
	for _, tc := range []struct {
		name, token string
		uid         uint64
	}{
		{"owner_list_is_private", ownerToken, ownerID},
		{"other_user_list_is_private", otherToken, otherID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := request(http.MethodGet, "/me/videos?page=1&page_size=6", tc.token, nil)
			if status != 200 {
				t.Fatalf("expected 200, got %d", status)
			}
			var list struct {
				Videos []Video `json:"videos"`
			}
			if err := json.Unmarshal(body, &list); err != nil || len(list.Videos) != 1 || list.Videos[0].AuthorID != tc.uid {
				t.Fatal("personal list must only return current user's video")
			}
		})
	}
	t.Run("personal_list_without_token", func(t *testing.T) {
		if status, _ := request(http.MethodGet, "/me/videos", "", nil); status != 401 {
			t.Errorf("expected 401, got %d", status)
		}
	})
	t.Run("personal_list_invalid_page", func(t *testing.T) {
		if status, _ := request(http.MethodGet, "/me/videos?page=0", ownerToken, nil); status != 400 {
			t.Errorf("expected 400, got %d", status)
		}
	})
	t.Run("public_list_includes_both_users", func(t *testing.T) {
		status, body := request(http.MethodGet, "/videos?page=1&page_size=100", "", nil)
		if status != 200 {
			t.Fatalf("expected 200, got %d", status)
		}
		var list struct {
			Videos []Video `json:"videos"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		foundOwner, foundOther := false, false
		for _, item := range list.Videos {
			foundOwner = foundOwner || item.AuthorID == ownerID
			foundOther = foundOther || item.AuthorID == otherID
		}
		if !foundOwner || !foundOther {
			t.Error("public library must include both users' new videos")
		}
	})
	for _, tc := range []struct {
		name, target, token string
		want                int
	}{
		{"like_first_time", path + "/like", ownerToken, 204},
		{"like_repeat", path + "/like", ownerToken, 204},
		{"like_other_user", path + "/like", otherToken, 204},
		{"like_without_token", path + "/like", "", 401},
		{"like_invalid_token", path + "/like", "invalid", 401},
		{"like_invalid_id", "/videos/abc/like", ownerToken, 400},
		{"like_zero_id", "/videos/0/like", ownerToken, 400},
		{"like_negative_id", "/videos/-1/like", ownerToken, 400},
		{"like_missing_video", "/videos/18446744073709551615/like", ownerToken, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := request(http.MethodPut, tc.target, tc.token, nil)
			if status != tc.want {
				t.Errorf("expected %d, got %d", tc.want, status)
			}
			if tc.want == 204 && len(body) != 0 {
				t.Error("204 must have no body")
			}
		})
	}
	t.Run("like_unique_per_user_and_video", func(t *testing.T) {
		for _, uid := range []uint64{ownerID, otherID} {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM video_likes WHERE user_id = ? AND video_id = ?", uid, published.ID).Scan(&count); err != nil || count != 1 {
				t.Error("each user must have exactly one like for the video")
			}
		}
	})
	// The owner likes a second video: cancelling the first must not remove it.
	var otherVideoID uint64
	if err := db.QueryRow("SELECT id FROM videos WHERE author_id = ? AND title = ?", otherID, prefix+"_other").Scan(&otherVideoID); err != nil {
		t.Fatal("cannot find second video fixture")
	}
	if status, _ := request(http.MethodPut, "/videos/"+strconv.FormatUint(otherVideoID, 10)+"/like", ownerToken, nil); status != 204 {
		t.Fatal("cannot prepare like on second video")
	}
	for _, tc := range []struct {
		name, target, token string
		want                int
	}{
		{"unlike_once", path + "/like", ownerToken, 204},
		{"unlike_repeat", path + "/like", ownerToken, 204},
		{"unlike_without_token", path + "/like", "", 401},
		{"unlike_invalid_token", path + "/like", "invalid", 401},
		{"unlike_invalid_id", "/videos/abc/like", ownerToken, 400},
		{"unlike_zero_id", "/videos/0/like", ownerToken, 400},
		{"unlike_negative_id", "/videos/-1/like", ownerToken, 400},
		{"unlike_missing_video", "/videos/18446744073709551615/like", ownerToken, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := request(http.MethodDelete, tc.target, tc.token, nil)
			if status != tc.want {
				t.Errorf("expected %d, got %d", tc.want, status)
			}
			if tc.want == 204 && len(body) != 0 {
				t.Error("204 must have no body")
			}
		})
	}
	t.Run("status_query_separates_total_and_identity_without_writes", func(t *testing.T) {
		for _, tc := range []struct {
			token string
			liked bool
		}{{ownerToken, false}, {otherToken, true}} {
			for i := 0; i < 2; i++ {
				status, body := request(http.MethodGet, path+"/like", tc.token, nil)
				if status != 200 {
					t.Fatalf("expected 200, got %d", status)
				}
				var result struct {
					Count uint64 `json:"like_count"`
					Liked bool   `json:"liked"`
				}
				if err := json.Unmarshal(body, &result); err != nil || result.Count != 1 || result.Liked != tc.liked {
					t.Fatalf("expected count 1 and liked %v, got %s", tc.liked, body)
				}
			}
		}
	})
	t.Run("status_query_missing_video", func(t *testing.T) {
		if status, _ := request(http.MethodGet, "/videos/18446744073709551615/like", ownerToken, nil); status != 404 {
			t.Errorf("expected 404, got %d", status)
		}
	})
	t.Run("unlike_only_removes_current_users_target", func(t *testing.T) {
		for _, tc := range []struct {
			uid, vid uint64
			want     int
		}{
			{ownerID, published.ID, 0},
			{otherID, published.ID, 1},
			{ownerID, otherVideoID, 1},
		} {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM video_likes WHERE user_id = ? AND video_id = ?", tc.uid, tc.vid).Scan(&count); err != nil || count != tc.want {
				t.Errorf("like count for user %d/video %d: expected %d, got %d (error %v)", tc.uid, tc.vid, tc.want, count, err)
			}
		}
	})
	cases := []struct {
		name, method, path, token string
		want                      int
	}{
		{"without_token", http.MethodDelete, path, "", 401},
		{"invalid_token", http.MethodDelete, path, "invalid", 401},
		{"invalid_id", http.MethodDelete, "/videos/abc", ownerToken, 400},
		{"zero_id", http.MethodDelete, "/videos/0", ownerToken, 400},
		{"other_user_cannot_delete", http.MethodDelete, path, otherToken, 404},
		{"video_survives_unauthorized_attempts", http.MethodGet, path, "", 200},
		{"owner_can_delete", http.MethodDelete, path, ownerToken, 204},
		{"video_is_gone", http.MethodGet, path, "", 404},
		{"repeat_delete", http.MethodDelete, path, ownerToken, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, body := request(tc.method, tc.path, tc.token, nil)
			if got != tc.want {
				t.Errorf("expected status %d, got %d", tc.want, got)
			}
			if tc.want == http.StatusNoContent && len(body) != 0 {
				t.Error("204 response must have an empty body")
			}
		})
	}
	t.Run("video_deletion_cascades_likes", func(t *testing.T) {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM video_likes WHERE video_id = ?", published.ID).Scan(&count); err != nil || count != 0 {
			t.Error("deleting the video must remove its likes")
		}
	})
	t.Run("video_deletion_cascades_comments", func(t *testing.T) {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM video_comments WHERE video_id = ?", published.ID).Scan(&count); err != nil || count != 0 {
			t.Error("deleting the video must remove its comments")
		}
	})
}
