package video

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gostream/internal/database"
)

func TestListExpiredPendingInvalidLimit(t *testing.T) {
	// Nil DB proves validation happens before any database access.
	repo := &SQLUploadRepository{}
	for _, limit := range []int{-1, 0, 1001} {
		if _, err := repo.ListExpiredPending(context.Background(), time.Now(), limit); err == nil {
			t.Errorf("limit %d must be rejected", limit)
		}
	}
}

func TestListExpiredPendingDatabaseFailure(t *testing.T) {
	db := sql.OpenDB(&uploadTestStore{})
	db.Close()
	if _, err := (&SQLUploadRepository{DB: database.WrapSQL(db)}).ListExpiredPending(context.Background(), time.Now(), 100); err == nil {
		t.Fatal("database errors must not be reported as an empty successful list")
	}
}

func TestListExpiredPendingIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 for local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local database config")
	}
	db, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured database")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Prepare isolated records atomically; no file is created or deleted.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "expiry_" + hex.EncodeToString(random[:])
	result, err := tx.ExecContext(ctx, "INSERT INTO users(username, password_hash) VALUES (?, ?)", prefix, "test fixture, not a login account")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Release fixture locks even if setup failed before Commit.
		tx.Rollback()
		// Exact fixture owner only; this does not touch existing user data.
		if _, err := db.Exec("DELETE FROM videos WHERE author_id=?", uid); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec("DELETE FROM uploads WHERE user_id=?", uid); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec("DELETE FROM users WHERE id=?", uid); err != nil {
			t.Error(err)
		}
	}()
	before := time.Date(2001, 1, 1, 0, 0, 0, 0, time.Local)
	old := before.Add(-time.Hour)
	type fixture struct {
		id       uint64
		status   string
		selected bool
	}
	fixtures := make(map[string]fixture)
	for _, tc := range []struct {
		name, kind, status string
		at                 time.Time
		selected           bool
	}{
		{"video_old", "video", "pending", old, true},
		{"cover_old", "cover", "pending", old, true},
		{"published", "video", "published", old, false},
		{"deleting", "video", "deleting", old, false},
		{"recent", "video", "pending", before.Add(time.Second), false},
		{"at_boundary", "video", "pending", before, false},
		{"referenced_video", "video", "pending", old, false},
		{"referenced_cover", "cover", "pending", old, false},
		{"unknown_type", "other", "pending", old, false},
	} {
		r, err := tx.ExecContext(ctx, "INSERT INTO uploads(user_id,saved_name,file_type,status,created_at) VALUES (?,?,?,?,?)", uid, prefix+tc.name, tc.kind, tc.status, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		id, err := r.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		fixtures[prefix+tc.name] = fixture{uint64(id), tc.status, tc.selected}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO videos(author_id,title,play_url,cover_url) VALUES (?,?,?,?)", uid, prefix,
		"/media/videos/"+prefix+"referenced_video", "/media/covers/"+prefix+"referenced_cover"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	repo := &SQLUploadRepository{DB: database.WrapSQL(db)}
	items, err := repo.ListExpiredPending(ctx, before, 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for i, item := range items {
		if i > 0 && (item.CreatedAt.Before(items[i-1].CreatedAt) || (item.CreatedAt.Equal(items[i-1].CreatedAt) && item.ID < items[i-1].ID)) {
			t.Error("candidates must be ordered by timestamp then ID")
		}
		if f, ok := fixtures[item.SavedName]; ok {
			if !f.selected {
				t.Errorf("unsafe cleanup candidate: %s", item.SavedName)
			}
			if item.ID != f.id || item.UserID != uint64(uid) || item.Status != "pending" || !item.CreatedAt.Equal(old) {
				t.Error("incorrect scanned upload fields")
			}
			seen[item.SavedName] = true
		}
	}
	for name, f := range fixtures {
		if f.selected && !seen[name] {
			t.Errorf("missing expired candidate %s", name)
		}
	}
	limited, err := repo.ListExpiredPending(ctx, before, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != items[0].ID {
		t.Fatal("limit must return the first candidate only")
	}
	for _, f := range fixtures {
		var status string
		if err := db.QueryRowContext(ctx, "SELECT status FROM uploads WHERE id=?", f.id).Scan(&status); err != nil || status != f.status {
			t.Error("candidate lookup must not modify or delete uploads")
		}
	}
	empty, err := repo.ListExpiredPending(ctx, time.Date(1971, 1, 1, 0, 0, 0, 0, time.Local), 100)
	if err != nil || empty == nil {
		t.Fatal("empty results must be a non-nil slice")
	}
}
