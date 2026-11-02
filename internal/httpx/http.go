package httpx

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// BodyLimit limits the complete request, including multipart boundaries.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		defer func() {
			if c.Request.MultipartForm != nil {
				c.Request.MultipartForm.RemoveAll()
			}
		}()
		c.Next()
	}
}

// RegisterMedia exposes individual files, without directory listings or cached
// open handles. net/http ServeContent supplies HEAD and byte-range support.
func RegisterMedia(router *gin.Engine, prefix, dir string) {
	handler := func(c *gin.Context) {
		name := strings.TrimPrefix(c.Param("filepath"), "/")
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			c.Status(http.StatusNotFound)
			return
		}
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			c.Status(http.StatusNotFound)
			return
		}
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
	}
	router.GET(prefix+"/*filepath", handler)
	router.HEAD(prefix+"/*filepath", handler)
}
