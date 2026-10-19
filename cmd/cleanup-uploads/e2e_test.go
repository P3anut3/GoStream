package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

// This test runs the actual command binary against a new disposable database
// and a temporary project directory. It never executes cleanup against .env's DB.
func TestCleanupCommandE2E(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1; requires permission to create a temporary MySQL database")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local database configuration")
	}
	cfg, err := mysql.ParseDSN(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("invalid local database configuration")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "cleanup-uploads")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cleanup binary: %v\n%s", err, output)
	}

	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	// Only this generated name is ever passed to CREATE/DROP DATABASE.
	databaseName := "gostream_cleanup_test_" + hex.EncodeToString(random[:])
	adminConfig := cfg.Clone()
	adminConfig.DBName = ""
	admin, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatal("cannot initialize database admin connection")
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+databaseName+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal("cannot create isolated E2E database; CREATE DATABASE permission is required")
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE `"+databaseName+"`"); err != nil {
			t.Errorf("failed to drop isolated E2E database %s", databaseName)
		}
	})
	testConfig := cfg.Clone()
	testConfig.DBName = databaseName
	db, err := sql.Open("mysql", testConfig.FormatDSN())
	if err != nil {
		t.Fatal("cannot initialize isolated database")
	}
	t.Cleanup(func() { db.Close() })
	schema, err := os.ReadFile("../../scripts/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	tables := 0
	for _, statement := range strings.Split(string(schema), ";") {
		statement = strings.TrimSpace(statement)
		if !strings.HasPrefix(statement, "CREATE TABLE ") {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create isolated tables: %v", err)
		}
		tables++
	}
	if tables != 5 {
		t.Fatalf("expected current five-table schema, created %d tables", tables)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("# E2E configuration is supplied through the child environment.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	videoDir, coverDir := filepath.Join(root, "uploads", "videos"), filepath.Join(root, "uploads", "covers")
	for _, dir := range []string{videoDir, coverDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.ExecContext(ctx, "INSERT INTO users(username,password_hash) VALUES (?,?)", "cleanup_e2e", "test fixture, not a login account")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		id                 uint64
		path, name, status string
		remove, missing    bool
	}
	fixtures := make([]fixture, 0, 7)
	for i, tc := range []struct {
		kind, status    string
		age             time.Duration
		remove, missing bool
	}{
		{"video", "pending", 48 * time.Hour, true, false},
		{"cover", "pending", 48 * time.Hour, true, false},
		{"video", "pending", time.Hour, false, false},
		{"video", "published", 48 * time.Hour, false, false},
		{"cover", "pending", 48 * time.Hour, false, false}, // protect a reference even if status is stale
		{"video", "deleting", 48 * time.Hour, true, false},
		{"cover", "deleting", 48 * time.Hour, true, true},
	} {
		dir, ext := videoDir, ".mp4"
		if tc.kind == "cover" {
			dir, ext = coverDir, ".png"
		}
		name := fmt.Sprintf("%032x%s", i+1, ext)
		path := filepath.Join(dir, name)
		if !tc.missing {
			if err := os.WriteFile(path, []byte("isolated E2E media"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		r, err := db.ExecContext(ctx, "INSERT INTO uploads(user_id,saved_name,file_type,status,created_at) VALUES (?,?,?,?,?)", uid, name, tc.kind, tc.status, time.Now().Add(-tc.age))
		if err != nil {
			t.Fatal(err)
		}
		id, err := r.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, fixture{uint64(id), path, name, tc.status, tc.remove, tc.missing})
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO videos(author_id,title,play_url,cover_url) VALUES (?,?,?,?)", uid, "protected E2E video", "/media/videos/"+fixtures[3].name, "/media/covers/"+fixtures[4].name); err != nil {
		t.Fatal(err)
	}
	runBinary := func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = root
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "GOSTREAM_DSN=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GOSTREAM_DSN="+testConfig.FormatDSN())
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated cleanup command failed: %v\n%s", err, output)
		}
		return string(output)
	}
	checkFixture := func(t *testing.T, f fixture, deleted bool) {
		t.Helper()
		var status string
		err := db.QueryRowContext(ctx, "SELECT status FROM uploads WHERE id=?", f.id).Scan(&status)
		if deleted {
			if err != sql.ErrNoRows {
				t.Errorf("upload %d should be removed", f.id)
			}
		} else if err != nil || status != f.status {
			t.Errorf("upload %d was changed unexpectedly", f.id)
		}
		data, fileErr := os.ReadFile(f.path)
		if deleted || f.missing {
			if !os.IsNotExist(fileErr) {
				t.Errorf("expected absent file %s", f.name)
			}
		} else if fileErr != nil || !bytes.Equal(data, []byte("isolated E2E media")) {
			t.Errorf("protected file %s changed", f.name)
		}
	}
	t.Run("preview_preserves_all_files_and_records", func(t *testing.T) {
		output := runBinary(t)
		if !strings.Contains(output, "预览模式") || !strings.Contains(output, "待重试记录：2 条") || !strings.Contains(output, "过期上传候选：2 条") {
			t.Fatalf("unexpected preview: %s", output)
		}
		for _, f := range fixtures {
			checkFixture(t, f, false)
		}
	})
	if t.Failed() {
		return
	}
	t.Run("execute_cleans_only_eligible_and_retries", func(t *testing.T) {
		output := runBinary(t, "--execute", "--limit=100")
		if !strings.Contains(output, "成功=4 跳过=0 失败=0") {
			t.Fatalf("unexpected execute result: %s", output)
		}
		for _, f := range fixtures {
			checkFixture(t, f, f.remove)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM videos").Scan(&count); err != nil || count != 1 {
			t.Fatal("published video must remain")
		}
	})
	if t.Failed() {
		return
	}
	t.Run("repeat_execute_is_safe", func(t *testing.T) {
		output := runBinary(t, "--execute")
		if !strings.Contains(output, "成功=0 跳过=0 失败=0") {
			t.Fatalf("unexpected repeated result: %s", output)
		}
		for _, f := range fixtures {
			checkFixture(t, f, f.remove)
		}
	})
}
