package video

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveVideo(t *testing.T) {
	dir := t.TempDir()
	content := []byte("test file content")

	first, err := saveVideo(bytes.NewReader(content), dir)
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	saved, err := os.ReadFile(filepath.Join(dir, first))
	if err != nil {
		t.Fatalf("读取已保存文件失败：%v", err)
	}

	if !bytes.Equal(saved, content) {
		t.Error("保存前后的内容不一致")
	}

	second, err := saveVideo(bytes.NewReader(content), dir)
	if err != nil {
		t.Fatalf("第二次保存失败：%v", err)
	}

	if first == second {
		t.Error("两次保存不应该使用相同文件名")
	}
}
