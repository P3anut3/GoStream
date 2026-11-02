package httpx

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gostream/internal/video"
)

func TestUnknownLengthMultipartLimit(t *testing.T) {
	for _, cover := range []bool{false, true} {
		name := "video"
		if cover {
			name = "cover"
		}
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "fixture.mp4")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(make([]byte, 1024)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.Use(BodyLimit(64))
			router.Use(func(c *gin.Context) { c.Set("userID", uint64(1)); c.Next() })
			handler := &video.Handler{UploadDir: t.TempDir(), CoverDir: t.TempDir()}
			if cover {
				router.POST("/upload", handler.UploadCover)
			} else {
				router.POST("/upload", handler.Upload)
			}
			req := httptest.NewRequest(http.MethodPost, "/upload", &body)
			req.ContentLength = -1
			req.Header.Set("Content-Type", writer.FormDataContentType())
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("expected 413 for streamed oversized request, got %d: %s", resp.Code, resp.Body)
			}
		})
	}
}
