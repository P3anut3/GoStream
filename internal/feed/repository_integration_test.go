package feed

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gostream/internal/database"
)

// A session-local temporary table shadows videos on one dedicated connection.
// No application video or user record is inserted, modified, or deleted.
func TestListLatestMySQLIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 for local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local database config")
	}
	pool, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured MySQL")
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("cannot obtain dedicated connection")
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `CREATE TEMPORARY TABLE videos (
		id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
		author_id BIGINT UNSIGNED NOT NULL,
		title VARCHAR(100) NOT NULL,
		play_url VARCHAR(1024) NOT NULL,
		cover_url VARCHAR(1024) NOT NULL,
		created_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		if _, err := conn.ExecContext(cleanupCtx, "DROP TEMPORARY TABLE IF EXISTS videos"); err != nil {
			t.Error(err)
		}
	}()
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &GORMRepository{DB: db}
	assertIDs := func(items []FeedItem, err error, want []uint64) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if items == nil {
			t.Fatal("expected non-nil slice")
		}
		ids := make([]uint64, len(items))
		for i := range items {
			ids[i] = items[i].ID
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("IDs = %v, want %v", ids, want)
		}
	}
	items, err := repo.ListLatest(ctx, nil, 2)
	assertIDs(items, err, []uint64{})
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	insert := func(id uint64, at time.Time) {
		t.Helper()
		_, err := conn.ExecContext(ctx, `INSERT INTO videos(id,author_id,title,play_url,cover_url,created_at) VALUES (?,1,?,?,?,?)`, id, "feed fixture", "/media/videos/fixture.mp4", "/cover.svg", at)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(99, stamp.Add(-time.Minute))
	insert(100, stamp)
	insert(101, stamp)
	insert(102, stamp)
	insert(103, stamp.Add(time.Minute))
	items, err = repo.ListLatest(ctx, nil, 3)
	assertIDs(items, err, []uint64{103, 102, 101}) // Service can request limit + 1.
	items, err = repo.ListLatest(ctx, nil, 2)
	assertIDs(items, err, []uint64{103, 102})
	boundary := &Position{CreatedAt: items[1].CreatedAt, ID: items[1].ID}
	insert(104, stamp.Add(2*time.Minute))
	// A cursor carries values, so its original row need not still exist.
	if _, err := conn.ExecContext(ctx, "DELETE FROM videos WHERE id = ?", 102); err != nil {
		t.Fatal(err)
	}
	items, err = repo.ListLatest(ctx, boundary, 2)
	assertIDs(items, err, []uint64{101, 100})
	boundary = &Position{CreatedAt: items[1].CreatedAt, ID: items[1].ID}
	items, err = repo.ListLatest(ctx, boundary, 2)
	assertIDs(items, err, []uint64{99})
	boundary = &Position{CreatedAt: items[0].CreatedAt, ID: items[0].ID}
	items, err = repo.ListLatest(ctx, boundary, 2)
	assertIDs(items, err, []uint64{})
	items, err = repo.ListLatest(ctx, nil, 2)
	assertIDs(items, err, []uint64{104, 103}) // Refresh sees the new head.
}
