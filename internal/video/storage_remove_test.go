package video

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveUploadedFile(t *testing.T) {
	for _, tc := range []struct{ name, kind, ext string }{
		{"video", "video", ".mp4"}, {"jpeg_cover", "cover", ".jpg"}, {"png_cover", "cover", ".png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			videoDir, coverDir := t.TempDir(), t.TempDir()
			name := strings.Repeat("a", 32) + tc.ext
			for _, dir := range []string{videoDir, coverDir} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("test media"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			upload := Upload{Status: "deleting", FileType: tc.kind, SavedName: name}
			if err := removeUploadedFile(upload, videoDir, coverDir); err != nil {
				t.Fatal(err)
			}
			target, untouched := videoDir, coverDir
			if tc.kind == "cover" {
				target, untouched = coverDir, videoDir
			}
			if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
				t.Fatal("selected file was not deleted")
			}
			if _, err := os.Stat(filepath.Join(untouched, name)); err != nil {
				t.Fatal("other media directory must remain untouched")
			}
			if err := removeUploadedFile(upload, videoDir, coverDir); err != nil {
				t.Fatal("repeat deletion must succeed", err)
			}
		})
	}
}

func TestRemoveUploadedFileRejectsUnsafeInput(t *testing.T) {
	name := strings.Repeat("a", 32) + ".mp4"
	for _, tc := range []struct {
		name, status, kind, filename string
		emptyDir                     bool
	}{
		{"pending", "pending", "video", name, false},
		{"published", "published", "video", name, false},
		{"unknown_type", "deleting", "other", name, false},
		{"empty_dir", "deleting", "video", name, true},
		{"traversal", "deleting", "video", "../" + name, false},
		{"windows_traversal", "deleting", "video", `..\` + name, false},
		{"absolute_path", "deleting", "video", `C:\uploads\` + name, false},
		{"non_hex", "deleting", "video", strings.Repeat("z", 32) + ".mp4", false},
		{"short_name", "deleting", "video", "a.mp4", false},
		{"wrong_video_extension", "deleting", "video", strings.Repeat("a", 32) + ".png", false},
		{"wrong_cover_extension", "deleting", "cover", name, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			videoDir, coverDir := t.TempDir(), t.TempDir()
			protected := filepath.Join(videoDir, name)
			if err := os.WriteFile(protected, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			configuredDir := videoDir
			if tc.emptyDir {
				configuredDir = ""
			}
			if err := removeUploadedFile(Upload{Status: tc.status, FileType: tc.kind, SavedName: tc.filename}, configuredDir, coverDir); err == nil {
				t.Fatal("unsafe input must be rejected")
			}
			if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
				t.Fatal("rejected operation must not change files")
			}
		})
	}
}

func TestRemoveUploadedFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("a", 32) + ".mp4"
	path := filepath.Join(dir, name)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeUploadedFile(Upload{Status: "deleting", FileType: "video", SavedName: name}, dir, t.TempDir()); err == nil {
		t.Fatal("directories must not be removed")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("directory must remain")
	}
}

func TestRemoveUploadedFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "protected.mp4")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("a", 32) + ".mp4"
	if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
		t.Skip("symlink creation unavailable on this host")
	}
	if err := removeUploadedFile(Upload{Status: "deleting", FileType: "video", SavedName: name}, dir, t.TempDir()); err == nil {
		t.Fatal("symlinks must be rejected")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatal("symlink target must remain unchanged")
	}
}
