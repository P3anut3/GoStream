package video

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// 删除磁盘文件
func removeUploadedFile(upload Upload, video_Dir, coverDir string) error {
	// 1、检查状态，只能处理已经取得清理权的记录
	if upload.Status != "deleting" {
		return fmt.Errorf("上传记录未出于 deleting 状态")
	}
	// 2、根据要删除的类型选择 服务端记录的文件类型目录
	var dir string
	switch upload.FileType {
	case "video":
		dir = video_Dir
	case "cover":
		dir = coverDir
	default:
		return fmt.Errorf("不支持的上传文件类型")
	}
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("上传目录不能为空")
	}
	// 3、检查文件名
	name := upload.SavedName
	ext := filepath.Ext(name)
	if upload.FileType == "video" && ext != ".mp4" {
		return fmt.Errorf("视频文件扩展名不正确")
	}
	if upload.FileType == "cover" && ext != ".png" && ext != ".jpg" {
		return fmt.Errorf("封面文件扩展名不正确")
	}
	stem := strings.TrimSuffix(name, ext)
	if len(stem) != 32 {
		return fmt.Errorf("上传文件名不符合系统命名规则")
	}
	if _, err := hex.DecodeString(stem); err != nil {
		return fmt.Errorf("上传文件名不是有效的十六进制字符串")
	}
	path := filepath.Join(dir, name)
	// 4、删除文件，只处理普通文件, 拒绝目录和符号链接
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查上传文件失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("上传路径不是普通文件")
	}
	if err := os.Remove(path); err != nil {
		//检查之后，文件可能被其他清理操作删除
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("删除上传文件失败: %w", err)
	}
	return nil
}

// 删除记录
