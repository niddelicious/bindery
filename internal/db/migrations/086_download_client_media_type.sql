-- +migrate Up
-- Per-download-client media type filter. When set to 'ebook' or 'audiobook',
-- the client is only eligible for downloads of that format. When empty or
-- 'both', the client is eligible for both formats.
ALTER TABLE download_clients ADD COLUMN media_type TEXT NOT NULL DEFAULT '';
