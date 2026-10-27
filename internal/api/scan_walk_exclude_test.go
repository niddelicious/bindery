package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/importer"
)

// Folder import uses the library scan's deny-list, not a blanket dot rule:
// Bindery's renamer keeps leading dots, so these are real book folders.
func TestEnumerateImportUnits_WalksDotNamedBookFolders(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"Agatha Christie/...And Then There Were None", "Britney Fan/..Baby One More Time",
		"Tatsuya Hamazaki/.hack Sign", ".38 Special/Hot Rocks",
	} {
		writeTestFile(t, filepath.Join(root, dir, "book.epub"))
	}
	units, _ := enumerateImportUnits(root, 100, nil, nil)
	if len(units) != 4 {
		t.Errorf("units = %+v, want all 4 books in dot-named folders", units)
	}
}

// Every entry of the library scan's deny-list is left out of folder import
// too; the two share importer.JunkLibraryDirName.
func TestEnumerateImportUnits_SkipsEachJunkFolder(t *testing.T) {
	for _, name := range []string{
		".torrents", ".Trash-1000", ".Trashes", ".incomplete", ".bindery-stage-abc",
		".AppleDouble", ".AppleDB", ".Spotlight-V100", ".fseventsd", ".TemporaryItems",
		".DocumentRevisions-V100", ".thumbnails", ".stfolder", ".stversions",
		".syncthing.x.tmp", ".sync", ".git", ".svn", ".recycle", ".snapshot", ".TORRENTS",
	} {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "Kari Nordmann", name, "b.epub"))
		if units, _ := enumerateImportUnits(root, 100, nil, nil); len(units) != 0 {
			t.Errorf("%s: units = %+v, want none", name, units)
		}
	}
}

// The configured download folders are left out of folder import when they sit
// inside the folder being imported, as the User Guide says. The reviewer's
// probe: a root holding Kari/b.epub and downloads/x/b.epub returned both.
func TestEnumerateImportUnits_SkipsExcludedDownloadFolders(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Kari", "downloads/x", "audio-dl/y"} {
		writeTestFile(t, filepath.Join(root, dir, "b.epub"))
	}
	excluded := []string{filepath.Join(root, "downloads"), filepath.Join(root, "audio-dl")}
	units, _ := enumerateImportUnits(root, 100, nil, excluded)
	if len(units) != 1 || !strings.HasPrefix(units[0].path, filepath.Join(root, "Kari")) {
		t.Errorf("units = %+v, want only the book under Kari", units)
	}
	// Importing a download folder itself still works: the root is never left out.
	units, _ = enumerateImportUnits(filepath.Join(root, "downloads"), 100, nil, excluded)
	if len(units) != 1 {
		t.Errorf("importing the download folder itself gave %+v, want its one book", units)
	}
}

// The handler passes its excluded folders to the walk, through a symlinked
// download folder too (Scan walks the symlink-resolved folder). Dropping the
// argument in Scan, or the resolved form in WithExcludedDirs, imports the
// download folder's contents.
func TestManualImportScan_LeavesExcludedDownloadFoldersOut(t *testing.T) {
	h, stub, _, _, _ := manualImportFixture(t)
	stub.lookupResult = importer.LookupResult{Match: "none", ParsedTitle: "x"}
	root := resolvedTempDir(t)
	for _, dir := range []string{"Kari", "downloads/x"} {
		writeTestFile(t, filepath.Join(root, dir, "b.epub"))
	}
	link := filepath.Join(t.TempDir(), "dl")
	if err := os.Symlink(filepath.Join(root, "downloads"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	h.WithExcludedDirs(link, "")

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(root))
	var resp ScanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || !strings.HasPrefix(resp.Items[0].Path, filepath.Join(root, "Kari")) {
		t.Errorf("items = %+v, want only the book under Kari", resp.Items)
	}
}
