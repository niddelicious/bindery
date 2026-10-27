package db

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// retentionRow writes one download in status whose terminal stamps and added_at
// are all at `at`, so the age rules have one unambiguous input.
func retentionRow(t *testing.T, r *DownloadRepo, guid string, status models.DownloadState, at time.Time, torrentID, nzoID *string) int64 {
	t.Helper()
	ctx := context.Background()
	d := &models.Download{GUID: guid, Title: guid, Status: models.StateGrabbed, Protocol: "torrent", TorrentID: torrentID, SABnzbdNzoID: nzoID}
	if err := r.Create(ctx, d); err != nil {
		t.Fatalf("create %s: %v", guid, err)
	}
	stamp := at.UTC()
	var imported, dead any
	switch status {
	case models.StateImported:
		imported = stamp
	case models.StateFailed, models.StateImportBlocked:
		dead = stamp
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE downloads SET status=?, added_at=?, imported_at=?, dead_at=? WHERE id=?`,
		status, stamp, imported, dead, d.ID); err != nil {
		t.Fatalf("stamp %s: %v", guid, err)
	}
	return d.ID
}

func guidsOf(t *testing.T, rows []models.Download) []string {
	t.Helper()
	out := make([]string, len(rows))
	for i, d := range rows {
		out[i] = d.GUID
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestListActive_StateTable pins which states the queue view shows. Every state
// is listed whatever its age except an imported one older than the bound; the
// states that wait for a person (failed, importFailed, importBlocked) must
// never age out of the view.
func TestListActive_StateTable(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-400 * 24 * time.Hour)
	bound := now.Add(-7 * 24 * time.Hour)

	// Every state ever defined, ancient. Only old imported may drop out.
	for _, s := range models.AllStates() {
		retentionRow(t, r, "old-"+string(s), s, old, nil, nil)
	}
	retentionRow(t, r, "recent-imported", models.StateImported, now.Add(-24*time.Hour), nil, nil)

	got, err := r.ListActive(ctx, bound)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, d := range got {
		have[d.GUID] = true
	}
	for _, s := range models.AllStates() {
		want := s != models.StateImported
		if have["old-"+string(s)] != want {
			t.Errorf("old %s row listed = %v, want %v", s, have["old-"+string(s)], want)
		}
	}
	if !have["recent-imported"] {
		t.Error("an import from yesterday must stay on the queue page")
	}
	// Newest first, like List.
	for i := 1; i < len(got); i++ {
		if got[i].AddedAt.After(got[i-1].AddedAt) {
			t.Fatalf("rows not newest first at %d", i)
		}
	}
}

// TestListActive_FallsBackThroughStamps: an imported row from before
// imported_at was stamped is aged by completed_at, then added_at.
func TestListActive_FallsBackThroughStamps(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now().UTC()
	bound := now.Add(-7 * 24 * time.Hour)

	id := retentionRow(t, r, "legacy", models.StateImported, now.Add(-400*24*time.Hour), nil, nil)
	if _, err := database.ExecContext(ctx, `UPDATE downloads SET imported_at=NULL, completed_at=? WHERE id=?`, now.Add(-time.Hour), id); err != nil {
		t.Fatal(err)
	}
	got, err := r.ListActive(ctx, bound)
	if err != nil || len(got) != 1 {
		t.Fatalf("legacy imported row with a recent completed_at: got %d rows, err %v; want it listed", len(got), err)
	}
}

func TestListActiveByUser_ScopesAndFilters(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (7, 'a', 'x', 'user'), (8, 'b', 'x', 'user')`); err != nil {
		t.Skipf("cannot seed users: %v", err)
	}
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now().UTC()
	mine := retentionRow(t, r, "mine-failed", models.StateFailed, now.Add(-300*24*time.Hour), nil, nil)
	theirs := retentionRow(t, r, "theirs-failed", models.StateFailed, now.Add(-300*24*time.Hour), nil, nil)
	oldImp := retentionRow(t, r, "mine-old-imported", models.StateImported, now.Add(-300*24*time.Hour), nil, nil)
	for id, owner := range map[int64]int64{mine: 7, theirs: 8, oldImp: 7} {
		if _, err := database.Exec(`UPDATE downloads SET owner_user_id=? WHERE id=?`, owner, id); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.ListActiveByUser(ctx, 7, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if g := guidsOf(t, got); !equalStrings(g, []string{"mine-failed"}) {
		t.Fatalf("user 7 sees %v, want only their failed row (not user 8's, not their old import)", g)
	}
}

func TestListByNzoIDs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now()
	retentionRow(t, r, "a", models.StateDownloading, now, nil, sp("nzo-1"))
	retentionRow(t, r, "dup-first", models.StateDownloading, now, nil, sp("nzo-2"))
	// Second row naming nzo-2: GetByNzoID returns the lower id, so must this.
	retentionRow(t, r, "dup-second", models.StateDownloading, now, nil, sp("nzo-2"))
	retentionRow(t, r, "none", models.StateDownloading, now, nil, nil)

	got, err := r.ListByNzoIDs(ctx, []string{"nzo-1", "nzo-2", "missing", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["nzo-1"].GUID != "a" || got["nzo-2"].GUID != "dup-first" {
		t.Fatalf("got %v, want nzo-1 -> a and nzo-2 -> dup-first only", got)
	}
	single, _ := r.GetByNzoID(ctx, "nzo-2")
	if single == nil || single.GUID != got["nzo-2"].GUID {
		t.Errorf("ListByNzoIDs and GetByNzoID disagree on a duplicated id")
	}

	// More ids than one chunk must still answer, in more than one query.
	many := make([]string, 1200)
	for i := range many {
		many[i] = "x" + string(rune('a'+i%26)) + time.Duration(i).String()
	}
	many = append(many, "nzo-1")
	got, err = r.ListByNzoIDs(ctx, many)
	if err != nil || got["nzo-1"].GUID != "a" {
		t.Fatalf("a real id past the first chunk was lost: %v err %v", got["nzo-1"].GUID, err)
	}
}

// TestTrimTerminal pins what retention deletes and what it leaves.
func TestTrimTerminal(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-200 * 24 * time.Hour)
	cutoff := now.Add(-90 * 24 * time.Hour)

	// Rows that go.
	retentionRow(t, r, "old-imported", models.StateImported, old, nil, nil)
	retentionRow(t, r, "old-failed", models.StateFailed, old, nil, nil)
	// Rows that stay: too recent, and every state that is live or waits for a person.
	retentionRow(t, r, "new-imported", models.StateImported, now.Add(-10*24*time.Hour), nil, nil)
	retentionRow(t, r, "new-failed", models.StateFailed, now.Add(-10*24*time.Hour), nil, nil)
	for _, s := range models.AllStates() {
		if s == models.StateImported || s == models.StateFailed {
			continue
		}
		retentionRow(t, r, "old-"+string(s), s, old, nil, nil)
	}

	// A history row and a blocklist row naming the trimmed release survive.
	if _, err := database.Exec(`INSERT INTO history (event_type, source_title, data) VALUES ('grabbed', 'old-imported', '{"guid":"old-imported"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO blocklist (guid, title, reason) VALUES ('old-failed', 'old-failed', 'test')`); err != nil {
		t.Fatal(err)
	}

	n, err := r.TrimTerminal(ctx, cutoff, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed %d rows, want 2", n)
	}
	all, _ := r.List(ctx)
	have := map[string]bool{}
	for _, d := range all {
		have[d.GUID] = true
	}
	for _, gone := range []string{"old-imported", "old-failed"} {
		if have[gone] {
			t.Errorf("%s should have been trimmed", gone)
		}
	}
	for _, kept := range []string{"new-imported", "new-failed", "old-" + string(models.StateImportBlocked), "old-" + string(models.StateImportFailed), "old-" + string(models.StateDownloading), "old-" + string(models.StateImportExternal)} {
		if !have[kept] {
			t.Errorf("%s must survive retention", kept)
		}
	}
	var hist, bl int
	database.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&hist)
	database.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&bl)
	if hist != 1 || bl != 1 {
		t.Errorf("history rows %d, blocklist rows %d; retention must not touch either", hist, bl)
	}
}

// TestTrimTerminal_KeepsRowsThatShareAJobWithSurvivors is the #3051 guard: an
// old imported row stays while a row that is not being trimmed names the same
// torrent or usenet job, because downloader.OtherDownloadUsingClientJob needs
// it to decide whether removing the newer row may take the job from the client.
func TestTrimTerminal_KeepsRowsThatShareAJobWithSurvivors(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-200 * 24 * time.Hour)
	cutoff := now.Add(-90 * 24 * time.Hour)

	retentionRow(t, r, "old-shares-torrent", models.StateImported, old, sp("hash1"), nil)
	retentionRow(t, r, "live-torrent", models.StateDownloading, now, sp("hash1"), nil)
	retentionRow(t, r, "old-shares-nzo", models.StateImported, old, nil, sp("nzo9"))
	retentionRow(t, r, "new-imported-nzo", models.StateImported, now.Add(-time.Hour), nil, sp("nzo9"))
	// Two old rows on one job: nothing left needs either, both go.
	retentionRow(t, r, "old-pair-a", models.StateImported, old, sp("hash2"), nil)
	retentionRow(t, r, "old-pair-b", models.StateImported, old, sp("hash2"), nil)
	// An old row with no job id, beside another blank one, shares nothing.
	retentionRow(t, r, "old-blank-a", models.StateImported, old, sp(""), sp(""))
	retentionRow(t, r, "live-blank", models.StateDownloading, now, sp(""), sp(""))

	n, err := r.TrimTerminal(ctx, cutoff, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := r.List(ctx)
	have := map[string]bool{}
	for _, d := range all {
		have[d.GUID] = true
	}
	for _, kept := range []string{"old-shares-torrent", "old-shares-nzo"} {
		if !have[kept] {
			t.Errorf("%s shares a job with a surviving row and must be kept", kept)
		}
	}
	for _, gone := range []string{"old-pair-a", "old-pair-b", "old-blank-a"} {
		if have[gone] {
			t.Errorf("%s shares a job with nothing that survives and should be trimmed", gone)
		}
	}
	if n != 3 {
		t.Errorf("removed %d, want 3", n)
	}
}

// TestTrimTerminal_Batches: a large backlog is deleted in several statements of
// at most batchSize rows, not one, and a cancelled context stops between them.
func TestTrimTerminal_Batches(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	now := time.Now().UTC()
	old := now.Add(-200 * 24 * time.Hour)
	for i := 0; i < 25; i++ {
		retentionRow(t, r, "bulk-"+time.Duration(i).String(), models.StateImported, old, nil, nil)
	}
	cutoff := now.Add(-90 * 24 * time.Hour)

	// The context is cancelled while the trim waits out the pause after its
	// first batch of 10, so it must stop there and say why.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	n, err := r.TrimTerminal(ctx, cutoff, 10, time.Hour)
	if err == nil {
		t.Fatalf("a cancelled context must surface an error between batches, removed %d", n)
	}
	if n != 10 {
		t.Fatalf("first batch removed %d rows, want exactly the batch size 10", n)
	}

	n, err = r.TrimTerminal(context.Background(), cutoff, 10, 0)
	if err != nil || n != 15 {
		t.Fatalf("remaining backlog: removed %d err %v, want 15", n, err)
	}
}

// TestTrimTerminal_AgeIsWhenTheRowFinished: the cutoff measures when a row
// became imported or failed, not when it was added. A release grabbed a year
// ago whose import finished yesterday, or whose failure was recorded yesterday
// (a re-grab resets added_at, but a row can also sit queued for months), must
// survive. It also reads stamps in the form SQLite's own defaults write
// ("2006-01-02 15:04:05"), which older rows carry.
func TestTrimTerminal_AgeIsWhenTheRowFinished(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()
	now := time.Now().UTC()
	yearAgo := now.Add(-400 * 24 * time.Hour)
	yesterday := now.Add(-24 * time.Hour)
	cutoff := now.Add(-90 * 24 * time.Hour)

	imp := retentionRow(t, r, "added-long-ago-imported-yesterday", models.StateImported, yearAgo, nil, nil)
	if _, err := database.ExecContext(ctx, `UPDATE downloads SET imported_at=? WHERE id=?`, yesterday, imp); err != nil {
		t.Fatal(err)
	}
	fail := retentionRow(t, r, "added-long-ago-failed-yesterday", models.StateFailed, yearAgo, nil, nil)
	if _, err := database.ExecContext(ctx, `UPDATE downloads SET dead_at=? WHERE id=?`, yesterday, fail); err != nil {
		t.Fatal(err)
	}
	legacy := retentionRow(t, r, "legacy-no-imported-at", models.StateImported, yearAgo, nil, nil)
	if _, err := database.ExecContext(ctx, `UPDATE downloads SET imported_at=NULL, completed_at=? WHERE id=?`, yesterday, legacy); err != nil {
		t.Fatal(err)
	}
	sqliteForm := "2006-01-02 15:04:05"
	oldText := retentionRow(t, r, "text-stamp-old", models.StateImported, yearAgo, nil, nil)
	if _, err := database.ExecContext(ctx, `UPDATE downloads SET added_at=?, imported_at=? WHERE id=?`,
		yearAgo.Format(sqliteForm), yearAgo.Format(sqliteForm), oldText); err != nil {
		t.Fatal(err)
	}
	newText := retentionRow(t, r, "text-stamp-new", models.StateImported, yearAgo, nil, nil)
	if _, err := database.ExecContext(ctx, `UPDATE downloads SET added_at=?, imported_at=? WHERE id=?`,
		yesterday.Format(sqliteForm), yesterday.Format(sqliteForm), newText); err != nil {
		t.Fatal(err)
	}

	n, err := r.TrimTerminal(ctx, cutoff, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := r.List(ctx)
	have := map[string]bool{}
	for _, d := range all {
		have[d.GUID] = true
	}
	for _, kept := range []string{"added-long-ago-imported-yesterday", "added-long-ago-failed-yesterday", "legacy-no-imported-at", "text-stamp-new"} {
		if !have[kept] {
			t.Errorf("%s finished recently and must survive", kept)
		}
	}
	if have["text-stamp-old"] || n != 1 {
		t.Errorf("text-stamp-old should be the only row trimmed: removed %d, still present %v", n, have["text-stamp-old"])
	}
}

// TestListByNzoIDs_ChunksPastTheVariableLimit: SQLite refuses a statement with
// more bound variables than its limit (32766 in this driver), so a poll over a
// history larger than that only works if ids go out in chunks. Ids that sit in
// the first, a middle and the last chunk must all come back.
func TestListByNzoIDs_ChunksPastTheVariableLimit(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	r := NewDownloadRepo(database)
	ctx := context.Background()
	const n = 33500
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "id" + strconv.Itoa(i)
	}
	for _, i := range []int{0, 16000, n - 1} {
		retentionRow(t, r, "g"+ids[i], models.StateDownloading, time.Now(), nil, sp(ids[i]))
	}
	got, err := r.ListByNzoIDs(ctx, ids)
	if err != nil {
		t.Fatalf("%d ids in one call: %v", n, err)
	}
	if len(got) != 3 {
		t.Errorf("found %d rows, want the 3 that exist", len(got))
	}
}
