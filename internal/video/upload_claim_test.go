package video

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gostream/internal/database"
)

func TestClaimExpiredIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_INTEGRATION_TEST") != "1" {
		t.Skip("set GOSTREAM_INTEGRATION_TEST=1 for local MySQL")
	}
	config, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local config")
	}
	db, err := database.OpenDB(config["GOSTREAM_DSN"])
	if err != nil {
		t.Fatal("cannot connect to configured database")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "claim_" + hex.EncodeToString(random[:])
	r, err := db.ExecContext(ctx, "INSERT INTO users(username,password_hash) VALUES (?,?)", prefix, "test fixture, not a login account")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := r.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, query := range []string{"DELETE FROM videos WHERE author_id=?", "DELETE FROM uploads WHERE user_id=?", "DELETE FROM users WHERE id=?"} {
			if _, err := db.Exec(query, uid); err != nil {
				t.Error(err)
			}
		}
	}()
	repo := &SQLUploadRepository{DB: database.WrapSQL(db)}
	before := time.Now().Truncate(time.Second).Add(-24 * time.Hour)
	create := func(t *testing.T, name, kind, status string, at time.Time) uint64 {
		t.Helper()
		r, err := db.ExecContext(ctx, "INSERT INTO uploads(user_id,saved_name,file_type,status,created_at) VALUES (?,?,?,?,?)", uid, prefix+name, kind, status, at)
		if err != nil {
			t.Fatal(err)
		}
		id, err := r.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return uint64(id)
	}
	for _, tc := range []struct {
		name, kind, status string
		at                 time.Time
		referenced, want   bool
	}{
		{"old_video", "video", "pending", before.Add(-time.Hour), false, true},
		{"old_cover", "cover", "pending", before.Add(-time.Hour), false, true},
		{"published", "video", "published", before.Add(-time.Hour), false, false},
		{"deleting", "video", "deleting", before.Add(-time.Hour), false, false},
		{"recent", "video", "pending", before.Add(time.Second), false, false},
		{"boundary", "video", "pending", before, false, false},
		{"referenced_video", "video", "pending", before.Add(-time.Hour), true, false},
		{"referenced_cover", "cover", "pending", before.Add(-time.Hour), true, false},
		{"unknown_type", "other", "pending", before.Add(-time.Hour), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := create(t, tc.name, tc.kind, tc.status, tc.at)
			if tc.referenced {
				play, cover := "https://example.invalid/fixture", "/cover.svg"
				if tc.kind == "video" {
					play = "/media/videos/" + prefix + tc.name
				} else {
					cover = "/media/covers/" + prefix + tc.name
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO videos(author_id,title,play_url,cover_url) VALUES (?,?,?,?)", uid, prefix, play, cover); err != nil {
					t.Fatal(err)
				}
			}
			upload, claimed, err := repo.ClaimExpired(ctx, id, before)
			if err != nil {
				t.Errorf("unexpected claim error: %v", err)
			}
			if claimed != tc.want {
				t.Errorf("expected claimed %v, got %v", tc.want, claimed)
			}
			wantStatus := tc.status
			if tc.want {
				wantStatus = "deleting"
				if upload.ID != id || upload.Status != wantStatus || upload.UserID != uint64(uid) {
					t.Error("incorrect claimed upload")
				}
			}
			var status string
			if err := db.QueryRowContext(ctx, "SELECT status FROM uploads WHERE id=?", id).Scan(&status); err != nil || status != wantStatus {
				t.Error("incorrect persisted status")
			}
			if tc.want {
				if _, claimed, err := repo.ClaimExpired(ctx, id, before); err != nil || claimed {
					t.Error("repeat claim must not acquire deletion rights again")
				}
			}
		})
	}
	t.Run("retry_list_only_returns_deleting_in_id_order", func(t *testing.T) {
		items, err := repo.ListDeleting(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			t.Fatal("expected previously claimed fixtures")
		}
		for i, item := range items {
			if item.Status != "deleting" || (i > 0 && item.ID <= items[i-1].ID) {
				t.Error("retry list must only contain deleting records in ID order")
			}
		}
		limited, err := repo.ListDeleting(ctx, 1)
		if err != nil || len(limited) != 1 || limited[0].ID != items[0].ID {
			t.Fatal("retry query must honor limit")
		}
		if _, err := repo.ListDeleting(ctx, 0); err == nil {
			t.Error("retry query must reject invalid limit")
		}
	})
	t.Run("retry_lookup_and_delete_are_restricted_to_deleting", func(t *testing.T) {
		for _, status := range []string{"pending", "published", "deleting"} {
			id := create(t, "retry_"+status, "video", status, before.Add(-time.Hour))
			upload, found, err := repo.GetDeleting(ctx, id)
			if err != nil || found != (status == "deleting") {
				t.Fatalf("incorrect retry eligibility for %s: %v", status, err)
			}
			if found && (upload.ID != id || upload.Status != status) {
				t.Error("incorrect retry record")
			}
			if err := repo.DeleteClaimed(ctx, id); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads WHERE id=?", id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if status == "deleting" {
				want = 0
			}
			if count != want {
				t.Errorf("delete guard failed for %s", status)
			}
			if status == "deleting" {
				if _, found, err := repo.GetDeleting(ctx, id); err != nil || found {
					t.Error("completed record must not be retried")
				}
				if err := repo.DeleteClaimed(ctx, id); err != nil {
					t.Error("repeat record deletion must succeed")
				}
			}
		}
	})
	t.Run("missing_record", func(t *testing.T) {
		if _, claimed, err := repo.ClaimExpired(ctx, ^uint64(0), before); err != nil || claimed {
			t.Errorf("missing record must not be claimed: %v", err)
		}
	})
	t.Run("concurrent_claim_has_one_winner", func(t *testing.T) {
		id := create(t, "concurrent", "video", "pending", before.Add(-time.Hour))
		type outcome struct {
			claimed bool
			err     error
		}
		start, done := make(chan struct{}), make(chan outcome, 2)
		for i := 0; i < 2; i++ {
			go func() { <-start; _, claimed, err := repo.ClaimExpired(ctx, id, before); done <- outcome{claimed, err} }()
		}
		close(start)
		winners := 0
		for i := 0; i < 2; i++ {
			result := <-done
			if result.err != nil {
				t.Error(result.err)
			}
			if result.claimed {
				winners++
			}
		}
		if winners != 1 {
			t.Errorf("expected one cleanup owner, got %d", winners)
		}
	})
	t.Run("publication_lock_prevents_claim", func(t *testing.T) {
		id := create(t, "publication", "video", "pending", before.Add(-time.Hour))
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var found uint64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM uploads WHERE id=? FOR UPDATE", id).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE uploads SET status='published' WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			claimed bool
			err     error
		}
		done := make(chan outcome, 1)
		go func() { _, claimed, err := repo.ClaimExpired(ctx, id, before); done <- outcome{claimed, err} }()
		select {
		case result := <-done:
			t.Fatalf("claim must wait for the publication row lock: %+v", result)
		case <-time.After(50 * time.Millisecond):
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		result := <-done
		if result.err != nil || result.claimed {
			t.Errorf("published upload must not be claimed after lock is released: %v", result.err)
		}
	})
}
