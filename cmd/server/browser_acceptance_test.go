package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	"gostream/internal/database"
)

// Manual browser harness, not a claim of automated UI success. All data is disposable.
// Opt in with GOSTREAM_BROWSER_ACCEPTANCE=1 and an explicit local MP4 fixture path.
// POST /__acceptance/finish on the printed localhost URL ends the environment.
func TestBrowserAcceptanceEnvironment(t *testing.T) {
	if os.Getenv("GOSTREAM_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("manual isolated browser environment")
	}
	fixture := os.Getenv("GOSTREAM_BROWSER_VIDEO")
	if fixture == "" {
		t.Fatal("provide an explicit MP4 test fixture")
	}
	media, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal("cannot read MP4 fixture")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read database config")
	}
	cfg, err := mysql.ParseDSN(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("invalid database config")
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "gostream_browser_test_" + hex.EncodeToString(random[:])
	adminCfg := cfg.Clone()
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot initialize admin")
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal("cannot create disposable database")
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.ExecContext(c, "DROP DATABASE `"+name+"`"); err != nil {
			t.Errorf("cannot drop disposable database %s", name)
		}
	}()
	testCfg := cfg.Clone()
	testCfg.DBName = name
	pool, err := sql.Open("mysql", testCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema, err := os.ReadFile("../../scripts/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(string(schema), ";") {
		statement = strings.TrimSpace(statement)
		if strings.HasPrefix(statement, "CREATE TABLE ") {
			if _, err := pool.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "web/dist"), os.DirFS("../../web/dist")); err != nil {
		t.Fatal("build Vue frontend before browser acceptance: ", err)
	}
	fixtureCopy := filepath.Join(root, "acceptance.mp4")
	if err := os.WriteFile(fixtureCopy, media, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	router := newRouter(database.WrapSQL(pool), []byte("browser-test-only-jwt-key-at-least-32-bytes"), newTestFeedCursor(t), newTestStorage(t), nil, false)
	finish := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__acceptance/finish", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204); once.Do(func() { close(finish) }) })
	mux.Handle("/", router)
	server := httptest.NewServer(mux)
	defer server.Close()
	fmt.Printf("BROWSER_ACCEPTANCE_URL=%s\nBROWSER_ACCEPTANCE_VIDEO=%s\n", server.URL, fixtureCopy)
	select {
	case <-finish:
		t.Log("manual browser environment closed; see recorded UI evidence for assertions")
	case <-time.After(8 * time.Minute):
		t.Fatal("browser environment timed out")
	}
}
