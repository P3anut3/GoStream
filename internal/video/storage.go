package video

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func saveVideo(src io.Reader, dir string) (string, error) {
	return saveMedia(src, dir, ".mp4")
}

// extension must come from server-side validation, never from a client path.
func saveMedia(src io.Reader, dir, extension string) (string, error) {
	if extension != ".mp4" && extension != ".jpg" && extension != ".png" {
		return "", fmt.Errorf("unsupported media extension: %q", extension)
	}
	//创建保存目录,已经存在 则不重复创建
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	// 生成随机文件名
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	filename := hex.EncodeToString(randomBytes) + extension
	path := filepath.Join(dir, filename)
	//创建目标文件(文件必须不存在，避免覆盖已有内容)
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	// 保存失败时，关闭并删除不完整文件
	completed := false
	defer func() {
		dst.Close()
		if !completed {
			os.Remove(path)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil { //将文件内容写入磁盘
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	completed = true
	return filename, nil
}
