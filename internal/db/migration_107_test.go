package db

import (
	"strings"
	"testing"
)

// Migration 107 adds four indexes, and each one has to be picked by the query
// it was added for. The plans are read on an empty migrated database, so the
// planner decides on the schema alone, the way it does on a user's library
// that has never been ANALYZEd.
func TestMigrate107PerfIndexesExistAndAreUsed(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cases := []struct {
		name, index, query string
		args               []any
		noTempSort         bool
	}{
		{
			name:  "history by event type, newest first",
			index: "idx_history_event_type_created_at",
			query: `SELECT id FROM history WHERE event_type = ? ORDER BY created_at DESC, id DESC LIMIT 50 OFFSET 0`,
			args:  []any{"grabbed"}, noTempSort: true,
		},
		{
			name:  "history event type count",
			index: "idx_history_event_type_created_at",
			query: `SELECT COUNT(*) FROM history WHERE event_type = ?`,
			args:  []any{"grabbed"},
		},
		{
			name:  "pending releases for a book",
			index: "idx_pending_releases_book_id",
			query: `SELECT id FROM pending_releases WHERE book_id = ?`,
			args:  []any{1},
		},
		{
			name:  "downloads newest first",
			index: "idx_downloads_added_at",
			query: `SELECT id FROM downloads ORDER BY added_at DESC`, noTempSort: true,
		},
		{
			name:  "authors using a quality profile",
			index: "idx_authors_quality_profile_id",
			query: `SELECT COUNT(*) FROM authors WHERE quality_profile_id = ?`,
			args:  []any{1},
		},
	}
	for _, c := range cases {
		var n int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, c.index).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: index %s is missing after migrating", c.name, c.index)
			continue
		}
		plan := strings.Join(explainPlan(t, database, c.query, c.args...), "\n")
		if !strings.Contains(plan, c.index) {
			t.Errorf("%s: plan does not use %s:\n%s", c.name, c.index, plan)
		}
		if c.noTempSort && strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s: plan still sorts in a temp b-tree:\n%s", c.name, plan)
		}
	}
}

// The migration is safe to run again over a database that already has the
// indexes, for example one an operator created by hand.
func TestMigrate107IsIdempotent(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	sqlBytes, err := migrationsFS.ReadFile("migrations/107_perf_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sqlBytes), "-- +migrate Down")
	for _, stmt := range strings.Split(up, ";") {
		var lines []string
		for _, l := range strings.Split(stmt, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "--") {
				lines = append(lines, l)
			}
		}
		if q := strings.TrimSpace(strings.Join(lines, "\n")); q != "" {
			mustExec(t, database, q)
		}
	}
}
