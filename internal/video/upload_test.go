package video

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gostream/internal/database"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gostream/internal/auth"

	"github.com/gin-gonic/gin"
	"gostream/internal/httpx"
	ut "gostream/internal/testutil"
)

func TestUpload(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	token, err := auth.GenerateToken(123, secret)
	if err != nil {
		t.Fatalf("generate test token: %v", err)
	}
	// Minimal ftyp header for MIME sniffing; not a complete playable video.
	mp4Bytes := func(size int) []byte {
		content := make([]byte, size)
		copy(content, []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'})
		return content
	}

	cases := []struct {
		name, field, filename, token string
		content                      []byte
		want                         int
	}{
		{"file_metadata", "file", "demo.mp4", token, mp4Bytes(1024), 200},
		{"unicode_filename", "file", "测试视频.mp4", token, mp4Bytes(1024), 200},
		{"uppercase_extension", "file", "demo.MP4", token, mp4Bytes(1024), 200},
		{"wrong_extension", "file", "demo.txt", token, mp4Bytes(1024), 415},
		{"fake_mp4", "file", "fake.mp4", token, []byte("not a video"), 415},
		{"database_failure", "file", "demo.mp4", token, mp4Bytes(1024), 500},
		{"empty_file", "file", "empty.mp4", token, nil, 400},
		{"below_size_limit", "file", "below.mp4", token, mp4Bytes(100*1024*1024 - 1), 200},
		{"exact_size_limit", "file", "exact.mp4", token, mp4Bytes(100 * 1024 * 1024), 200},
		{"above_size_limit", "file", "above.mp4", token, make([]byte, 100*1024*1024+1), 413},
		{"missing_file", "", "", token, nil, 400},
		{"wrong_field", "video", "demo.mp4", token, []byte("sample"), 400},
		{"without_token", "file", "demo.mp4", "", []byte("sample"), 401},
		{"invalid_token", "file", "demo.mp4", "invalid", []byte("sample"), 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := gin.New()
			store := &uploadTestStore{dir: dir, fail: tc.name == "database_failure"}
			db := sql.OpenDB(store)
			t.Cleanup(func() { db.Close() })
			handler := &Handler{DB: database.WrapSQL(db), UploadDir: dir}
			h.POST("/uploads/videos", auth.JWTAuth(secret), handler.Upload)
			httpx.RegisterMedia(h, "/media/videos", dir)
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
			resp := ut.PerformRequest(h, http.MethodPost, "/uploads/videos",
				&ut.Body{Body: bytes.NewReader(body.Bytes()), Len: body.Len()}, headers...).Result()
			if resp.StatusCode() != tc.want {
				t.Fatalf("expected status %d, got %d: %s", tc.want, resp.StatusCode(), resp.Body())
			}
			if tc.want == http.StatusOK {
				var metadata struct {
					Filename  string `json:"filename"`
					SavedName string `json:"saved_name"`
					PlayURL   string `json:"play_url"`
					Size      int64  `json:"size"`
				}
				if err := json.Unmarshal(resp.Body(), &metadata); err != nil {
					t.Fatalf("expected JSON filename and numeric size: %v", err)
				}
				if metadata.Filename != tc.filename || metadata.Size != int64(len(tc.content)) {
					t.Errorf("incorrect file metadata: got %+v, expected filename %q and size %d", metadata, tc.filename, len(tc.content))
				}
				if filepath.Base(metadata.SavedName) != metadata.SavedName || filepath.Ext(metadata.SavedName) != ".mp4" {
					t.Fatalf("invalid stored filename: %q", metadata.SavedName)
				}
				if random, err := hex.DecodeString(metadata.SavedName[:len(metadata.SavedName)-4]); err != nil || len(random) != 16 {
					t.Error("stored filename must contain 16 random bytes encoded as hex")
				}
				saved, err := os.ReadFile(filepath.Join(dir, metadata.SavedName))
				if err != nil {
					t.Fatalf("cannot read saved upload: %v", err)
				}
				if !bytes.Equal(saved, tc.content) {
					t.Error("saved content differs from upload; check that the file was rewound after MIME sniffing")
				}
				if metadata.PlayURL != "/media/videos/"+metadata.SavedName {
					t.Fatalf("incorrect play_url: %q", metadata.PlayURL)
				}
				if store.userID != 123 || store.savedName != metadata.SavedName {
					t.Errorf("upload record must use JWT user ID and saved filename: got %d, %q", store.userID, store.savedName)
				}
				// The media endpoint is public: no Authorization header is supplied.
				media := ut.PerformRequest(h, http.MethodGet, metadata.PlayURL, nil).Result()
				if media.StatusCode() != http.StatusOK {
					t.Fatalf("media GET expected 200, got %d", media.StatusCode())
				}
				if !bytes.Equal(media.Body(), tc.content) {
					t.Error("media response differs from the original uploaded bytes")
				}
				if got := string(media.Header.ContentType()); got != "video/mp4" {
					t.Errorf("expected video/mp4 response content type, got %q", got)
				}
				head := ut.PerformRequest(h, http.MethodHead, metadata.PlayURL, nil).Result()
				if head.StatusCode() != http.StatusOK || len(head.Body()) != 0 {
					t.Errorf("media HEAD expected 200 with no body, got status %d and %d bytes", head.StatusCode(), len(head.Body()))
				}
				partial := ut.PerformRequest(h, http.MethodGet, metadata.PlayURL, nil,
					ut.Header{Key: "Range", Value: "bytes=0-15"}).Result()
				if partial.StatusCode() != http.StatusPartialContent || !bytes.Equal(partial.Body(), tc.content[:16]) {
					t.Errorf("byte range request must return 206 and the requested bytes, got status %d", partial.StatusCode())
				}
			} else {
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(resp.Body(), &failure); err != nil || failure.Error == "" {
					t.Error("expected a JSON error message")
				}
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			wantFiles := 0
			if tc.want == http.StatusOK {
				wantFiles = 1
			}
			if len(files) != wantFiles {
				t.Errorf("expected %d stored files, got %d", wantFiles, len(files))
			}
			wantCalls := 0
			if tc.want == http.StatusOK || tc.name == "database_failure" {
				wantCalls = 1
			}
			if store.calls != wantCalls {
				t.Errorf("expected %d database writes, got %d", wantCalls, store.calls)
			}
			missing := ut.PerformRequest(h, http.MethodGet, "/media/videos/does-not-exist.mp4", nil).Result()
			if missing.StatusCode() != http.StatusNotFound {
				t.Errorf("missing media expected 404, got %d", missing.StatusCode())
			}
		})
	}
}

// A database/sql test driver: no real MySQL connection or test dependency needed.
// It checks the INSERT contract and that the file exists before recording it.
type uploadTestStore struct {
	dir, savedName string
	wantType       string
	userID         int64
	calls          int
	fail           bool
}

func (s *uploadTestStore) Connect(context.Context) (driver.Conn, error) {
	return &uploadTestConn{store: s}, nil
}
func (s *uploadTestStore) Driver() driver.Driver { return uploadTestDriver{} }

type uploadTestDriver struct{}

func (uploadTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type uploadTestConn struct{ store *uploadTestStore }

func (*uploadTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*uploadTestConn) Close() error { return nil }
func (*uploadTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected Begin")
}
func (c *uploadTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.store
	s.calls++
	if strings.ReplaceAll(strings.ReplaceAll(query, "`", ""), " ", "") != "INSERTINTOuploads(user_id,saved_name,file_type)VALUES(?,?,?)" || len(args) != 3 {
		return nil, errors.New("unexpected upload INSERT")
	}
	wantType := s.wantType
	if wantType == "" {
		wantType = "video"
	}
	if args[2].Value != wantType {
		return nil, errors.New("upload file type does not match its purpose")
	}
	userID, ok := args[0].Value.(int64)
	if !ok || userID != 123 {
		return nil, errors.New("incorrect JWT user ID")
	}
	name, ok := args[1].Value.(string)
	if !ok || filepath.Base(name) != name {
		return nil, errors.New("invalid stored filename")
	}
	if _, err := os.Stat(filepath.Join(s.dir, name)); err != nil {
		return nil, err
	}
	if s.fail {
		return nil, errors.New("simulated database write failure")
	}
	s.userID, s.savedName = userID, name
	return publishTransactionResult{}, nil
}
