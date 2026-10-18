package video

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gostream/internal/auth"
)

func TestUploadCover(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatal(err)
	}
	var pngBuffer, jpegBuffer bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&pngBuffer, picture); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegBuffer, picture, nil); err != nil {
		t.Fatal(err)
	}
	pngBytes, jpegBytes := pngBuffer.Bytes(), jpegBuffer.Bytes()
	padded := func(size int) []byte { b := make([]byte, size); copy(b, pngBytes); return b }
	for _, tc := range []struct {
		name, field, filename, token string
		content                      []byte
		want                         int
	}{
		{"png", "file", "cover.png", token, pngBytes, 200},
		{"jpeg", "file", "cover.jpg", token, jpegBytes, 200},
		{"uppercase_jpeg", "file", "封面.JPEG", token, jpegBytes, 200},
		{"below_limit", "file", "cover.png", token, padded(5*1024*1024 - 1), 200},
		{"exact_limit", "file", "cover.png", token, padded(5 * 1024 * 1024), 200},
		{"above_limit", "file", "cover.png", token, padded(5*1024*1024 + 1), 413},
		{"empty", "file", "cover.png", token, nil, 400},
		{"missing_file", "", "", token, nil, 400},
		{"wrong_field", "cover", "cover.png", token, pngBytes, 400},
		{"wrong_extension", "file", "cover.gif", token, pngBytes, 415},
		{"fake_png", "file", "cover.png", token, []byte("not an image"), 415},
		{"truncated_header", "file", "cover.png", token, pngBytes[:16], 415},
		{"mismatch", "file", "cover.jpg", token, pngBytes, 415},
		{"without_token", "file", "cover.png", "", pngBytes, 401},
		{"invalid_token", "file", "cover.png", "invalid", pngBytes, 401},
		{"database_failure", "file", "cover.png", token, pngBytes, 500},
		{"storage_failure", "file", "cover.png", token, pngBytes, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := &uploadTestStore{dir: dir, wantType: "cover", fail: tc.name == "database_failure"}
			db := sql.OpenDB(store)
			t.Cleanup(func() { db.Close() })
			coverDir := dir
			if tc.name == "storage_failure" {
				coverDir = filepath.Join(dir, "blocked")
				if err := os.WriteFile(coverDir, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h := server.New()
			handler := &Handler{DB: db, CoverDir: coverDir}
			h.POST("/uploads/covers", auth.JWTAuth(secret), handler.UploadCover)
			h.StaticFS("/media/covers", &app.FS{
				Root: dir, PathRewrite: app.NewPathSlashesStripper(2),
				GenerateIndexPages: false, CacheDuration: time.Millisecond,
			})
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if tc.field != "" {
				part, err := writer.CreateFormFile(tc.field, tc.filename)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write(tc.content); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			headers := []ut.Header{{Key: "Content-Type", Value: writer.FormDataContentType()}}
			if tc.token != "" {
				headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + tc.token})
			}
			resp := ut.PerformRequest(h.Engine, http.MethodPost, "/uploads/covers",
				&ut.Body{Body: bytes.NewReader(body.Bytes()), Len: body.Len()}, headers...).Result()
			if resp.StatusCode() != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, resp.StatusCode(), resp.Body())
			}
			wantCalls := 0
			if tc.want == 200 || tc.name == "database_failure" {
				wantCalls = 1
			}
			if store.calls != wantCalls {
				t.Errorf("expected %d database writes, got %d", wantCalls, store.calls)
			}
			if tc.want == 200 {
				var result struct {
					Filename  string `json:"filename"`
					SavedName string `json:"saved_name"`
					CoverURL  string `json:"cover_url"`
					Size      int64  `json:"size"`
				}
				if err := json.Unmarshal(resp.Body(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Filename != tc.filename || result.Size != int64(len(tc.content)) || store.userID != 123 || store.savedName != result.SavedName {
					t.Fatal("incorrect upload record or response")
				}
				ext := ".png"
				if bytes.Equal(tc.content, jpegBytes) {
					ext = ".jpg"
				}
				if filepath.Ext(result.SavedName) != ext || result.CoverURL != "/media/covers/"+result.SavedName {
					t.Fatal("incorrect cover path or extension")
				}
				if random, err := hex.DecodeString(result.SavedName[:len(result.SavedName)-len(ext)]); err != nil || len(random) != 16 {
					t.Fatal("expected random filename")
				}
				saved, err := os.ReadFile(filepath.Join(dir, result.SavedName))
				if err != nil || !bytes.Equal(saved, tc.content) {
					t.Fatal("saved bytes differ from upload")
				}
				media := ut.PerformRequest(h.Engine, http.MethodGet, result.CoverURL, nil).Result()
				if media.StatusCode() != 200 || !bytes.Equal(media.Body(), tc.content) {
					t.Fatal("cover must be publicly readable with original bytes")
				}
				mime := "image/png"
				if ext == ".jpg" {
					mime = "image/jpeg"
				}
				if string(media.Header.ContentType()) != mime {
					t.Fatal("incorrect cover content type")
				}
			} else {
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error == "" {
					t.Fatal("expected JSON error")
				}
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			wantFiles := 0
			if tc.want == 200 || tc.name == "storage_failure" {
				wantFiles = 1
			}
			if len(files) != wantFiles {
				t.Errorf("expected %d files, got %d", wantFiles, len(files))
			}
		})
	}
}
