package feed

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

// GOSTREAM_TEST_MYSQL_DSN must point to a disposable MySQL server. The test
// creates its own random database, applies the real 006 migration there and
// drops the database afterwards; it never reads .env or touches gostream.
func TestListFollowingMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("GOSTREAM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOSTREAM_TEST_MYSQL_DSN to a disposable MySQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := database.OpenDB(dsn)
	if err != nil {
		t.Fatal("cannot connect to the disposable MySQL server")
	}
	defer admin.Close()
	name := fmt.Sprintf("gostream_following_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.DBName = name
	config.ParseTime = true
	pool, err := database.OpenDB(config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// users and videos mirror scripts/schema.sql; follows comes from the real migration file.
	for _, ddl := range []string{
		`CREATE TABLE users (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			username VARCHAR(32) NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (id),
			UNIQUE KEY uk_users_username (username)
		)`,
		`CREATE TABLE videos (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			author_id BIGINT UNSIGNED NOT NULL,
			title VARCHAR(100) NOT NULL,
			play_url VARCHAR(1024) NOT NULL,
			cover_url VARCHAR(1024) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (id),
			KEY idx_videos_author_id (author_id),
			KEY idx_videos_created_at_id (created_at, id),
			CONSTRAINT fk_videos_author FOREIGN KEY (author_id) REFERENCES users(id)
		)`,
	} {
		if _, err := pool.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("../../scripts/migrations/006_follows.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("006 migration failed: %v", err)
	}
	// Users 1..5: A is the viewer, B and C are followed by A, D is followed only by E.
	if _, err := pool.ExecContext(ctx, "INSERT INTO users (id, username, password_hash) VALUES (1,'a','x'),(2,'b','x'),(3,'c','x'),(4,'d','x'),(5,'e','x')"); err != nil {
		t.Fatal(err)
	}
	const viewer, b, c, d, e uint64 = 1, 2, 3, 4, 5
	stamp := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		id, author uint64
		at         time.Time
	}{
		{101, b, stamp},
		{102, d, stamp}, // same time as the boundary, smaller ID, author not followed by A
		{103, c, stamp},
		{104, b, stamp.Add(time.Minute)},
		{105, d, stamp.Add(2 * time.Minute)},
	} {
		if _, err := pool.ExecContext(ctx, "INSERT INTO videos (id, author_id, title, play_url, cover_url, created_at) VALUES (?,?,?,?,?,?)",
			v.id, v.author, "following fixture", "/media/videos/f.mp4", "/cover.svg", v.at); err != nil {
			t.Fatal(err)
		}
	}
	follow := func(follower, followee uint64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, "INSERT INTO follows (follower_id, followee_id) VALUES (?, ?)", follower, followee); err != nil {
			t.Fatal(err)
		}
	}
	follow(viewer, b)
	follow(viewer, c)
	follow(e, d)

	repo := &GORMRepository{DB: database.WrapSQL(pool)}
	expectIDs := func(viewerID uint64, position *Position, limit int, want []uint64) []FeedItem {
		t.Helper()
		items, err := repo.ListFollowing(ctx, viewerID, position, limit)
		if err != nil {
			t.Fatal(err)
		}
		if items == nil {
			t.Fatal("expected a non-nil slice")
		}
		got := make([]uint64, len(items))
		for i := range items {
			got[i] = items[i].ID
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("viewer %d after %v: got %v, want %v", viewerID, position, got, want)
		}
		return items
	}
	// Only B's and C's videos, newest first; D's videos never appear for A.
	first := expectIDs(viewer, nil, 2, []uint64{104, 103})
	// Continuing from (stamp, 103) must not let video 102 (same time, smaller ID) slip in.
	boundary := &Position{CreatedAt: first[1].CreatedAt, ID: first[1].ID}
	expectIDs(viewer, boundary, 2, []uint64{101})
	expectIDs(viewer, &Position{CreatedAt: stamp, ID: 101}, 2, []uint64{})
	// Relations are directed: B follows nobody, E only sees D.
	expectIDs(b, nil, 10, []uint64{})
	expectIDs(e, nil, 10, []uint64{105, 102})
	// After unfollowing C, a new query no longer contains C's video.
	if _, err := pool.ExecContext(ctx, "DELETE FROM follows WHERE follower_id = ? AND followee_id = ?", viewer, c); err != nil {
		t.Fatal(err)
	}
	expectIDs(viewer, nil, 10, []uint64{104, 101})
}
