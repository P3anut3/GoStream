package video

import (
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// UploadCover accepts a JPEG/PNG file and records its authenticated owner.
func (h *Handler) UploadCover(ctx context.Context, c *app.RequestContext) {
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || !ok || userID == 0 {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "请求中缺少有效的用户身份"})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "请通过 file 字段上传封面"})
		return
	}
	if file.Size == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "不能上传空文件"})
		return
	}
	if file.Size > 5*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "封面大小不能超过 5 MiB"})
		return
	}
	extension := strings.ToLower(filepath.Ext(file.Filename))
	expectedFormat := ""
	switch extension {
	case ".jpg", ".jpeg":
		expectedFormat, extension = "jpeg", ".jpg"
	case ".png":
		expectedFormat = "png"
	default:
		c.JSON(http.StatusUnsupportedMediaType, map[string]string{"error": "封面只支持 JPEG、PNG"})
		return
	}
	src, err := file.Open()
	if err != nil {
		log.Printf("打开封面文件失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读取封面失败"})
		return
	}
	defer src.Close()
	// Decode only the image header: identify actual format without allocating pixels.
	config, format, err := image.DecodeConfig(src)
	if err != nil || format != expectedFormat || config.Width <= 0 || config.Height <= 0 {
		c.JSON(http.StatusUnsupportedMediaType, map[string]string{"error": "封面内容无效或与文件后缀不匹配"})
		return
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		log.Printf("重置封面读取位置失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读取封面失败"})
		return
	}
	savedName, err := saveMedia(src, h.CoverDir, extension)
	if err != nil {
		log.Printf("保存封面失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存封面失败"})
		return
	}
	_, err = h.DB.ExecContext(ctx,
		`INSERT INTO uploads (user_id, saved_name, file_type) VALUES (?, ?, ?)`,
		userID, savedName, "cover",
	)
	if err != nil {
		log.Printf("保存封面上传记录失败: %v", err)
		if removeErr := os.Remove(filepath.Join(h.CoverDir, savedName)); removeErr != nil {
			log.Printf("清理封面失败: %v", removeErr)
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存封面上传记录失败"})
		return
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"filename":   file.Filename,
		"saved_name": savedName,
		"size":       file.Size,
		"cover_url":  "/media/covers/" + savedName,
	})
}
