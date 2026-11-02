package feed

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	"gostream/internal/database"
)

func TestFeedIndexMigrationIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1; requires CREATE DATABASE permission")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local database configuration")
	}
	cfg, err := mysql.ParseDSN(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("invalid local database configuration")
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	// The only database ever created or dropped is this generated test name.
	name := "gostream_feed_index_test_" + hex.EncodeToString(random[:])
	adminConfig := cfg.Clone()
	adminConfig.DBName = ""
	admin, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatal("cannot initialize test admin connection")
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal("cannot create isolated test database; check MySQL and CREATE permission")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Errorf("cannot remove isolated database %s", name)
		}
	}()
	testConfig := cfg.Clone()
	testConfig.DBName = name
	pool, err := sql.Open("mysql", testConfig.FormatDSN())
	if err != nil {
		t.Fatal("cannot initialize isolated database connection")
	}
	defer pool.Close()
	schema, err := os.ReadFile("../../scripts/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	createTables := func(text string) {
		t.Helper()
		for _, statement := range strings.Split(text, ";") {
			statement = strings.TrimSpace(statement)
			// Never execute the production CREATE DATABASE or USE statements.
			if strings.HasPrefix(statement, "CREATE TABLE ") {
				if _, err := pool.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	checkIndex := func(want bool) {
		t.Helper()
		rows, err := pool.QueryContext(ctx, `SELECT SEQ_IN_INDEX,COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='videos' AND INDEX_NAME='idx_videos_created_at_id' ORDER BY SEQ_IN_INDEX`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var columns []string
		for rows.Next() {
			var seq int
			var column string
			if err := rows.Scan(&seq, &column); err != nil {
				t.Fatal(err)
			}
			if seq != len(columns)+1 {
				t.Fatal("incorrect index sequence")
			}
			columns = append(columns, column)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if want && !reflect.DeepEqual(columns, []string{"created_at", "id"}) {
			t.Fatalf("index columns = %v", columns)
		}
		if !want && len(columns) != 0 {
			t.Fatal("old schema unexpectedly contains Feed index")
		}
	}
	// Verify a clean install from the real current schema first.
	createTables(string(schema))
	checkIndex(true)
	if _, err := pool.ExecContext(ctx, "ALTER TABLE videos DROP INDEX idx_videos_created_at_id"); err != nil {
		t.Fatal(err)
	}
	checkIndex(false) // Existing installation before migration 005.
	if _, err := pool.ExecContext(ctx, "INSERT INTO users(id,username,password_hash) VALUES (1,'feed_index_fixture','not-a-login-password')"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT INTO videos(id,author_id,title,play_url,cover_url,created_at) VALUES (?,1,?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 2000; id++ {
		// Multiple rows share each second to exercise the unique tie-breaker.
		if _, err := statement.ExecContext(ctx, id, fmt.Sprintf("fixture %d", id), "/media/videos/fixture.mp4", "/cover.svg", base.Add(time.Duration(id/5)*time.Second)); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	repo := &GORMRepository{DB: database.WrapSQL(pool)}
	before, err := repo.ListLatest(ctx, nil, 11)
	if err != nil {
		t.Fatal(err)
	}
	boundary := &Position{CreatedAt: before[5].CreatedAt, ID: before[5].ID}
	beforeNext, err := repo.ListLatest(ctx, boundary, 11)
	if err != nil {
		t.Fatal(err)
	}
	const selectSQL = "SELECT id,author_id,title,play_url,cover_url,created_at FROM videos"
	firstSQL := selectSQL + " ORDER BY created_at DESC,id DESC LIMIT 11"
	nextSQL := selectSQL + " WHERE created_at < ? OR (created_at = ? AND id < ?) ORDER BY created_at DESC,id DESC LIMIT 11"
	explain := func(label, query string, args ...any) {
		t.Helper()
		rows, err := pool.QueryContext(ctx, "EXPLAIN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]sql.NullString, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			fields := map[string]string{}
			for i, column := range columns {
				if values[i].Valid {
					fields[column] = values[i].String
				} else {
					fields[column] = "NULL"
				}
			}
			t.Logf("%s: type=%s key=%s rows=%s Extra=%s", label, fields["type"], fields["key"], fields["rows"], fields["Extra"])
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	explain("before/first", firstSQL)
	explain("before/next", nextSQL, boundary.CreatedAt, boundary.CreatedAt, boundary.ID)
	migration, err := os.ReadFile("../../scripts/migrations/005_feed_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	checkIndex(true)
	if _, err := pool.ExecContext(ctx, "ANALYZE TABLE videos"); err != nil {
		t.Fatal(err)
	}
	explain("after/first", firstSQL)
	explain("after/next", nextSQL, boundary.CreatedAt, boundary.CreatedAt, boundary.ID)
	after, err := repo.ListLatest(ctx, nil, 11)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed first-page results: %v", err)
	}
	afterNext, err := repo.ListLatest(ctx, boundary, 11)
	if err != nil || !reflect.DeepEqual(beforeNext, afterNext) {
		t.Fatalf("migration changed subsequent-page results: %v", err)
	}
	if before[0].ID != 2000 || beforeNext[0].ID != 1994 {
		t.Fatal("unexpected fixture order")
	}
}
