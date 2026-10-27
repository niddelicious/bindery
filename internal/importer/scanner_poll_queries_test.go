package importer

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

// queryLog records every statement that reaches the database, so a test can
// assert how many queries a poll issued instead of how fast it ran.
type queryLog struct {
	mu      sync.Mutex
	queries []string
}

func (l *queryLog) add(q string) {
	l.mu.Lock()
	l.queries = append(l.queries, q)
	l.mu.Unlock()
}

// count returns how many recorded statements satisfy match.
func (l *queryLog) count(match func(string) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, q := range l.queries {
		if match(q) {
			n++
		}
	}
	return n
}

func (l *queryLog) reset() {
	l.mu.Lock()
	l.queries = nil
	l.mu.Unlock()
}

type countingDriver struct {
	inner driver.Driver
	log   *queryLog
}

func (d countingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, log: d.log}, nil
}

type countingConn struct {
	driver.Conn
	log *queryLog
}

func (c *countingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.log.add(q)
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.log.add(q)
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, q)
	}
	return c.Prepare(q)
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bc, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bc.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback for a driver without ConnBeginTx
}

var countingDriverSeq atomic.Int64

// openCountingDB returns a migrated on-disk database whose every statement is
// recorded in the returned log.
func openCountingDB(t *testing.T) (*sql.DB, *queryLog) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "count.db")
	// Migrate through the normal opener, then reopen through the counting driver.
	seed, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	counted := &queryLog{}
	// database/sql keeps a driver registered for the life of the process and
	// each database needs its own log, so every one gets a fresh name.
	name := "sqlite-counting-" + strconv.FormatInt(countingDriverSeq.Add(1), 10)
	sql.Register(name, countingDriver{inner: &sqlite.Driver{}, log: counted})
	database, err := sql.Open(name, path)
	if err != nil {
		t.Fatalf("open counting: %v", err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })
	return database, counted
}

// isNzoLookup matches the per-id lookup (GetByNzoID) and the batched one
// (ListByNzoIDs): any statement that selects downloads by sabnzbd_nzo_id.
func isNzoLookup(q string) bool {
	// The select list names sabnzbd_nzo_id too, so look for it in the WHERE.
	return strings.Contains(q, "FROM downloads") &&
		(strings.Contains(q, "WHERE sabnzbd_nzo_id=?") || strings.Contains(q, "WHERE sabnzbd_nzo_id IN ("))
}

// isWholeTableList matches a download read with no WHERE at all: the List that
// loads every row.
func isWholeTableList(q string) bool {
	return strings.Contains(q, "FROM downloads") && !strings.Contains(q, "WHERE") && strings.Contains(q, "ORDER BY added_at")
}

const pollQueueItems = 200

// TestCheckSABnzbdDownloads_OneLookupForTheWholePoll: a poll over 200 history
// slots looks the downloads up in ONE query, and reads no whole-table list.
// Before, it ran one GetByNzoID per slot and the closing
// blockStaleImportFailures loaded every row.
func TestCheckSABnzbdDownloads_OneLookupForTheWholePoll(t *testing.T) {
	database, counted := openCountingDB(t)
	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	slots := make([]sabnzbd.HistorySlot, pollQueueItems)
	for i := range slots {
		// A status the poller takes no action on, so only the lookups run.
		slots[i] = sabnzbd.HistorySlot{NzoID: fmt.Sprintf("SABnzbd_nzo_%d", i), Name: "Book", Status: "Queued"}
	}
	srv := httptest.NewServer(sabHistoryHandler(t, slots, nil))
	defer srv.Close()
	client := sabClient(t, ctx, clientRepo, srv.URL)
	// Half the slots have a row, half do not.
	for i := 0; i < pollQueueItems; i += 2 {
		nzo := slots[i].NzoID
		if err := dlRepo.Create(ctx, &models.Download{GUID: fmt.Sprintf("g-%d", i), Title: "Book", Status: models.StateDownloading,
			Protocol: "usenet", SABnzbdNzoID: &nzo, DownloadClientID: &client.ID}); err != nil {
			t.Fatal(err)
		}
	}

	s := NewScanner(dlRepo, clientRepo, db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")
	counted.reset()
	s.checkSABnzbdDownloads(ctx, client)

	if n := counted.count(isNzoLookup); n != 1 {
		t.Errorf("SABnzbd poll of %d slots ran %d nzo lookups, want 1", pollQueueItems, n)
	}
	if n := counted.count(isWholeTableList); n != 0 {
		t.Errorf("SABnzbd poll read the whole downloads table %d times, want 0", n)
	}
}

// TestCheckNZBGetDownloads_OneLookupForTheWholePoll is the NZBGet twin.
func TestCheckNZBGetDownloads_OneLookupForTheWholePoll(t *testing.T) {
	database, counted := openCountingDB(t)
	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	items := make([]nzbget.HistoryItem, pollQueueItems)
	for i := range items {
		items[i] = nzbget.HistoryItem{NZBID: 1000 + i, NZBName: "Book", Status: "DELETED/MANUAL"}
	}
	srv := httptest.NewServer(nzbgetHandler(t, items, nil))
	defer srv.Close()
	client := nzbgetClient(t, ctx, clientRepo, srv.URL)
	for i := 0; i < pollQueueItems; i += 2 {
		nzo := strconv.Itoa(1000 + i)
		if err := dlRepo.Create(ctx, &models.Download{GUID: fmt.Sprintf("g-%d", i), Title: "Book", Status: models.StateDownloading,
			Protocol: "usenet", SABnzbdNzoID: &nzo, DownloadClientID: &client.ID}); err != nil {
			t.Fatal(err)
		}
	}

	s := NewScanner(dlRepo, clientRepo, db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")
	counted.reset()
	s.checkNZBGetDownloads(ctx, client)

	if n := counted.count(isNzoLookup); n != 1 {
		t.Errorf("NZBGet poll of %d items ran %d nzo lookups, want 1", pollQueueItems, n)
	}
	if n := counted.count(isWholeTableList); n != 0 {
		t.Errorf("NZBGet poll read the whole downloads table %d times, want 0", n)
	}
}

// sabCompletionWrites runs one SABnzbd poll over the given history slots for a
// download that is still downloading, and returns how many statements wrote to
// the downloads table.
func sabCompletionWrites(t *testing.T, slots []sabnzbd.HistorySlot) int {
	t.Helper()
	database, counted := openCountingDB(t)
	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	srv := httptest.NewServer(sabHistoryHandler(t, slots, nil))
	defer srv.Close()
	client := sabClient(t, ctx, clientRepo, srv.URL)
	nzo := slots[0].NzoID
	if err := dlRepo.Create(ctx, &models.Download{GUID: "g-dup", Title: "Book", Status: models.StateDownloading,
		Protocol: "usenet", SABnzbdNzoID: &nzo, DownloadClientID: &client.ID}); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(dlRepo, clientRepo, db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")
	counted.reset()
	s.checkSABnzbdDownloads(ctx, client)
	return counted.count(func(q string) bool { return strings.Contains(q, "UPDATE downloads") })
}

// TestCheckSABnzbdDownloads_RepeatedSlotActsOnce: the batched lookup is a
// snapshot, so a history that names one nzo twice would act on the second slot
// with the stale "downloading" status the snapshot holds and run the completion
// again. The per-slot lookup used to see the first slot's write. A repeated slot
// must write exactly what a single one does.
func TestCheckSABnzbdDownloads_RepeatedSlotActsOnce(t *testing.T) {
	slot := sabnzbd.HistorySlot{NzoID: "SABnzbd_nzo_dup", Name: "Book", Status: "Completed", Path: t.TempDir()}
	once := sabCompletionWrites(t, []sabnzbd.HistorySlot{slot})
	if once == 0 {
		t.Fatal("fixture wrote nothing for a completed slot; the comparison would prove nothing")
	}
	twice := sabCompletionWrites(t, []sabnzbd.HistorySlot{slot, slot})
	if twice != once {
		t.Errorf("a repeated slot wrote %d times, a single one %d; the second must be skipped", twice, once)
	}
}

// TestRecoverInterruptedImports_NoWholeTableListPerRow: startup recovery used
// to load every download row once per recovered download (downloadByID listed
// the table and scanned it), which stalled startup for minutes on a large
// table. It must read each recovered row by id and list nothing.
func TestRecoverInterruptedImports_NoWholeTableListPerRow(t *testing.T) {
	database, counted := openCountingDB(t)
	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	const wedged = 25
	for i := 0; i < wedged; i++ {
		guid := fmt.Sprintf("w-%d", i)
		if err := dlRepo.Create(ctx, &models.Download{GUID: guid, Title: "Book", Status: models.StateGrabbed, Protocol: "usenet"}); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `UPDATE downloads SET status=? WHERE guid=?`, models.StateImportPending, guid); err != nil {
			t.Fatal(err)
		}
	}
	s := NewScanner(dlRepo, db.NewDownloadClientRepo(database), db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")
	counted.reset()
	s.RecoverInterruptedImports(ctx)

	if n := counted.count(isWholeTableList); n != 0 {
		t.Errorf("recovering %d rows read the whole downloads table %d times, want 0", wedged, n)
	}
	var recovered int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE status=?`, models.StateImportFailed).Scan(&recovered); err != nil || recovered != wedged {
		t.Errorf("recovered rows = %d (err %v), want %d re-queued as importFailed", recovered, err, wedged)
	}
}
