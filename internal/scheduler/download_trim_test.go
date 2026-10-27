package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func trimFixture(t *testing.T) (*db.DownloadRepo, func(guid string, status models.DownloadState, age time.Duration)) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewDownloadRepo(database)
	add := func(guid string, status models.DownloadState, age time.Duration) {
		t.Helper()
		d := &models.Download{GUID: guid, Title: guid, Status: models.StateGrabbed, Protocol: "usenet"}
		if err := repo.Create(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Add(-age)
		if _, err := database.Exec(`UPDATE downloads SET status=?, added_at=?, imported_at=?, dead_at=NULL WHERE id=?`,
			status, at, at, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	return repo, add
}

func remainingGUIDs(t *testing.T, repo *db.DownloadRepo) map[string]bool {
	t.Helper()
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, d := range all {
		out[d.GUID] = true
	}
	return out
}

func TestTrimFinishedDownloads_UsesTheRetentionWindow(t *testing.T) {
	repo, add := trimFixture(t)
	day := 24 * time.Hour
	add("imported-100d", models.StateImported, 100*day)
	add("imported-30d", models.StateImported, 30*day)
	add("failed-100d", models.StateFailed, 100*day)
	add("downloading-100d", models.StateDownloading, 100*day)

	s := &Scheduler{downloads: repo}
	s.WithDownloadRetention(90)
	s.trimFinishedDownloads(context.Background())

	got := remainingGUIDs(t, repo)
	if got["imported-100d"] || got["failed-100d"] {
		t.Errorf("rows past 90 days should be trimmed, left %v", got)
	}
	if !got["imported-30d"] || !got["downloading-100d"] {
		t.Errorf("a recent import and a live download must stay, left %v", got)
	}

	// A narrower window reaches the 30 day row too.
	s.WithDownloadRetention(7)
	s.trimFinishedDownloads(context.Background())
	if got = remainingGUIDs(t, repo); got["imported-30d"] || !got["downloading-100d"] {
		t.Errorf("7 day window: left %v", got)
	}
}

func TestTrimFinishedDownloads_ZeroKeepsEverything(t *testing.T) {
	repo, add := trimFixture(t)
	add("imported-1000d", models.StateImported, 1000*24*time.Hour)

	s := &Scheduler{downloads: repo}
	s.WithDownloadRetention(0)
	s.trimFinishedDownloads(context.Background())
	if !remainingGUIDs(t, repo)["imported-1000d"] {
		t.Fatal("retention 0 must keep every row")
	}
}

// TestStart_DownloadTrimJob: the job is on the cron only when retention is on
// and there is a store to trim, and it is the 6 hour one.
func TestStart_DownloadTrimJob(t *testing.T) {
	repo, _ := trimFixture(t)
	entries := func(days int, withStore bool) []cron.Entry {
		s := &Scheduler{cron: cron.New(cron.WithSeconds())}
		if withStore {
			s.downloads = repo
		}
		s.WithDownloadRetention(days)
		s.Start()
		e := s.cron.Entries()
		s.Stop()
		return e
	}
	const baseJobs = 5 // see TestStart_RegistersBaseJobs
	if n := len(entries(90, true)); n != baseJobs+1 {
		t.Errorf("retention 90 with a store: %d entries, want %d", n, baseJobs+1)
	}
	if e := entries(90, true); !hasEntryWithDelay(e, 6*time.Hour) {
		t.Error("the trim job should run every 6 hours")
	}
	if n := len(entries(0, true)); n != baseJobs {
		t.Errorf("retention 0 registered a job: %d entries, want %d", n, baseJobs)
	}
	if n := len(entries(90, false)); n != baseJobs {
		t.Errorf("no downloads store registered a job: %d entries, want %d", n, baseJobs)
	}
}
