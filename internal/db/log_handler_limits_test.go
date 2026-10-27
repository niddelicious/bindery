package db

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// The production batch limits are the documented ones. The other tests build
// handlers with explicit limits, so without this a change to the constants
// (one transaction per line, or an unbounded wait) would pass them all.
func TestNewLogSlogHandler_UsesDocumentedBatchLimits(t *testing.T) {
	repo := openLogTestDB(t)
	h := NewLogSlogHandler(repo, slog.LevelInfo)
	defer stopHandler(t, h)
	if h.maxRows != 100 || h.maxWait != 250*time.Millisecond {
		t.Errorf("batch limits are %d rows and %v, want 100 rows and 250ms", h.maxRows, h.maxWait)
	}
}

// Handle must drop, never block, when the writer is stalled. Holding the only
// pooled connection stalls the drain goroutine at BeginTx, so the channel fills
// and the records beyond its capacity have nowhere to go. The earlier flood
// test could not catch a blocking send because its writer kept up.
func TestLogHandler_HandleDropsWhileWriterIsStalled(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := newLogHandler(NewLogRepo(database), slog.LevelInfo, 100, time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3*logHandlerBufSize; i++ {
			_ = h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "stalled", 0))
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle blocked while the writer was stalled")
	}
	_ = conn.Close() // frees the connection so Stop can flush what was queued
	stopHandler(t, h)
}
