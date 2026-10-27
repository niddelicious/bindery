package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// A download folder inside the library must stay out of the "already in the
// library" check. Dropping either download dir from wireScanDownloadDirs makes
// a torrent copy count as an owned book (#3135).
func TestWireScanDownloadDirs_LeavesBothDownloadFoldersOut(t *testing.T) {
	lib := t.TempDir()
	author := filepath.Join(lib, "Kari Nordmann")
	ebookDL := filepath.Join(author, "incoming")
	audioDL := filepath.Join(author, "incoming-audio")
	for _, dir := range []string{ebookDL, audioDL, filepath.Join(author, "Kept")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{
		filepath.Join(ebookDL, "Fjellvinden.epub"),
		filepath.Join(audioDL, "Havgapet.epub"),
		filepath.Join(author, "Kept", "Skogens sang.epub"),
	} {
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 20000), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sc := importer.NewScanner(nil, nil, nil, nil, nil, lib, "", "", "", "")
	wireScanDownloadDirs(sc, &config.Config{DownloadDir: ebookDL, AudiobookDownloadDir: audioDL})

	snap := sc.SnapshotFinder()
	ctx := context.Background()
	for _, title := range []string{"Fjellvinden", "Havgapet"} {
		if got := snap.FindExisting(ctx, title, "Kari Nordmann", models.MediaTypeEbook); got != "" {
			t.Errorf("FindExisting(%q) = %q, want nothing: it exists only in a download folder", title, got)
		}
	}
	if got := snap.FindExisting(ctx, "Skogens sang", "Kari Nordmann", models.MediaTypeEbook); got == "" {
		t.Error("the library copy was not found; the test layout is wrong")
	}
}

func TestManualImportExcludedDirs(t *testing.T) {
	got := manualImportExcludedDirs(&config.Config{DownloadDir: "/dl", AudiobookDownloadDir: "/dl-audio"})
	want := []string{"/dl", "/dl-audio"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manualImportExcludedDirs = %v, want %v", got, want)
	}
}
