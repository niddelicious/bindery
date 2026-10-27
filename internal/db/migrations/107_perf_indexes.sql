-- +migrate Up
-- Indexes for four lookups that EXPLAIN QUERY PLAN showed walking a whole
-- table on a 200k history, 50k downloads library. IF NOT EXISTS on each so a
-- rerun is safe.
-- NOTE: the migration runner splits on semicolons, so keep them out of comments.

-- history filtered by event type, newest first. HistoryRepo.ListPaged and
-- ListByEventType filter on event_type and sort created_at DESC, id DESC.
-- Without this the planner walks idx_history_created_at_desc and filters, so
-- a rare event type reads the whole table (about 260 ms at 200k rows).
-- id DESC is part of the key because the page query orders by it as a
-- tiebreaker, and a key that stops at created_at leaves a temp b-tree sort
-- over every row of the event type.
CREATE INDEX IF NOT EXISTS idx_history_event_type_created_at
    ON history(event_type, created_at DESC, id DESC);

-- pending_releases looked up by book. Every per-book read and delete filters
-- on book_id, and so does the books ON DELETE CASCADE.
CREATE INDEX IF NOT EXISTS idx_pending_releases_book_id
    ON pending_releases(book_id);

-- downloads listed newest first (DownloadRepo.List and friends order by
-- added_at DESC). With the index the full list streams in order instead of
-- sorting every row in a temp b-tree first.
CREATE INDEX IF NOT EXISTS idx_downloads_added_at
    ON downloads(added_at);

-- authors filtered by quality profile. QualityProfileRepo.CountAuthorsUsing
-- and AuthorNamesUsing run the delete-in-use guard with
-- WHERE quality_profile_id = ?, which scanned every author.
CREATE INDEX IF NOT EXISTS idx_authors_quality_profile_id
    ON authors(quality_profile_id);

-- +migrate Down

DROP INDEX IF EXISTS idx_authors_quality_profile_id;
DROP INDEX IF EXISTS idx_downloads_added_at;
DROP INDEX IF EXISTS idx_pending_releases_book_id;
DROP INDEX IF EXISTS idx_history_event_type_created_at;
