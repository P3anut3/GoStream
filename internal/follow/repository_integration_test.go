package follow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gostream/internal/database"
)

// GOSTREAM_TEST_MYSQL_DSN must point to a disposable MySQL server. The test
// creates its own random database, applies the real 006 migration there and
// drops the database afterwards; it never reads .env or touches gostream.
func TestGORMRepositoryMySQLIntegration(t *testing.T) {
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
	name := fmt.Sprintf("gostream_follow_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()

	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.DBName = name
	pool, err := database.OpenDB(config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// users mirrors scripts/schema.sql; follows comes from the real migration file.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE users (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		username VARCHAR(32) NOT NULL,
		password_hash VARCHAR(255) NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_users_username (username)
	)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../scripts/migrations/006_follows.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("006 migration failed: %v", err)
	}

	newUser := func(username string) uint64 {
		t.Helper()
		result, err := pool.ExecContext(ctx, "INSERT INTO users (username, password_hash) VALUES (?, 'x')", username)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return uint64(id)
	}
	a, b, c := newUser("alice"), newUser("bob"), newUser("carol")
	const missing uint64 = 999999

	repo := &GORMRepository{DB: database.WrapSQL(pool)}
	expectFollowing := func(follower, followee uint64, want bool) {
		t.Helper()
		got, err := repo.IsFollowing(ctx, follower, followee)
		if err != nil {
			t.Fatalf("IsFollowing(%d, %d) error: %v", follower, followee, err)
		}
		if got != want {
			t.Fatalf("IsFollowing(%d, %d) = %v, want %v", follower, followee, got, want)
		}
	}

	if err := repo.Follow(ctx, a, b); err != nil {
		t.Fatalf("first follow: %v", err)
	}
	if err := repo.Follow(ctx, a, b); !errors.Is(err, ErrAlreadyFollowing) {
		t.Fatalf("duplicate follow: want ErrAlreadyFollowing, got %v", err)
	}
	if err := repo.Follow(ctx, a, missing); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing target: want ErrUserNotFound, got %v", err)
	}
	// The service rejects self-follows first; the CHECK constraint is the last line of defense.
	var mysqlErr *mysql.MySQLError
	if err := repo.Follow(ctx, a, a); !errors.As(err, &mysqlErr) || mysqlErr.Number != 3819 {
		t.Fatalf("self follow must hit CHECK 3819, got %v", err)
	}
	expectFollowing(a, b, true)
	expectFollowing(b, a, false) // relations are directed
	expectFollowing(a, c, false)
	if _, err := repo.IsFollowing(ctx, a, missing); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("status of missing target: want ErrUserNotFound, got %v", err)
	}

	if err := repo.Follow(ctx, b, a); err != nil {
		t.Fatalf("reverse follow: %v", err)
	}
	if err := repo.Unfollow(ctx, a, b); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	expectFollowing(a, b, false)
	expectFollowing(b, a, true) // removing A->B leaves B->A intact
	if err := repo.Unfollow(ctx, a, b); err != nil {
		t.Fatalf("repeated unfollow must succeed: %v", err)
	}
	if err := repo.Unfollow(ctx, a, missing); err != nil {
		t.Fatalf("unfollow of a missing target must succeed: %v", err)
	}
	var remaining int
	if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM follows").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("expected only B->A to remain, got %d rows", remaining)
	}
}
