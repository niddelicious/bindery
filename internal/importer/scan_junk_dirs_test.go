package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Bindery's own renamer keeps leading dots, so folders such as
// "...And Then There Were None", ".hack Sign" and an author ".38 Special" are
// real library folders. The scan skips a deny-list of junk and sync folders,
// not everything that starts with a dot (#3135 review).

var junkDirExamples = []string{
	".torrents", ".Trash-1000", ".trash-0", ".Trashes", ".incomplete",
	".bindery-stage-abc123", ".AppleDouble", ".AppleDB", ".Spotlight-V100",
	".fseventsd", ".TemporaryItems", ".DocumentRevisions-V100", ".thumbnails",
	".stfolder", ".stversions", ".syncthing.Book.epub.tmp", ".sync", ".git",
	".svn", ".recycle", ".snapshot",
	"@eaDir", "#recycle", ".@__thumb", ".zfs", ".snapshots", "lost+found",
	// Volumes on macOS and Windows are case-insensitive.
	".TORRENTS", ".SPOTLIGHT-v100", ".Git",
}

var dotLibraryDirs = []string{
	"...And Then There Were None", "..Baby One More Time", ".hack Sign", ".38 Special",
	// Close to a junk name, but not on the list.
	".torrents2", ".gitbook", ".synchronize", ".Trash", ".hidden-library-shelf",
}

func TestJunkLibraryDirName(t *testing.T) {
	for _, name := range junkDirExamples {
		if !JunkLibraryDirName(name) {
			t.Errorf("JunkLibraryDirName(%q) = false, want true", name)
		}
	}
	for _, name := range append([]string{"Kari Nordmann", "downloads", "Trash"}, dotLibraryDirs...) {
		if JunkLibraryDirName(name) {
			t.Errorf("JunkLibraryDirName(%q) = true, want false", name)
		}
	}
}

func TestWalkRoot_SkipsEachJunkFolder(t *testing.T) {
	for _, name := range junkDirExamples {
		root := t.TempDir()
		dir := filepath.Join(root, "Kari Nordmann", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeEpubAt(t, filepath.Join(dir, "Fjellvinden.epub"), "Fjellvinden", "Kari Nordmann", "")
		_ = walkRoot(root, nil, func(path string, info os.FileInfo, err error) error {
			if strings.HasSuffix(path, ".epub") {
				t.Errorf("%s: walked %q inside the folder", name, path)
			}
			return nil
		})
	}
}

// dotFolderLibrary lays out one book per dot-named folder, as the renamer
// would, each under a folder named for the dot shape being tested.
func dotFolderLibrary(t *testing.T) (root string, books map[string][2]string) {
	t.Helper()
	root = t.TempDir()
	books = map[string][2]string{
		// title -> {author, path of its file}
		"...And Then There Were None": {"Agatha Christie", filepath.Join("Agatha Christie", "...And Then There Were None", "...And Then There Were None.epub")},
		"..Baby One More Time":        {"Britney Fan", filepath.Join("Britney Fan", "..Baby One More Time", "..Baby One More Time.epub")},
		".hack Sign":                  {"Tatsuya Hamazaki", filepath.Join("Tatsuya Hamazaki", ".hack Sign", ".hack Sign.epub")},
		"Hot Rocks":                   {".38 Special", filepath.Join(".38 Special", "Hot Rocks", "Hot Rocks.epub")},
	}
	for title, b := range books {
		p := filepath.Join(root, b[1])
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEpubAt(t, p, title, b[0], "")
	}
	return root, books
}

func TestLibrarySnapshot_FindsBooksInDotNamedFolders(t *testing.T) {
	root, books := dotFolderLibrary(t)
	snap := NewLibrarySnapshot(root, "")
	for title, b := range books {
		want := filepath.Join(root, b[1])
		if got := snap.FindExisting(context.Background(), title, b[0], models.MediaTypeEbook); got != want {
			t.Errorf("FindExisting(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestScanLibrary_WalksDotNamedFolders(t *testing.T) {
	s, _, _, _, settings, libraryDir, ctx := unmatchedFixture(t)
	_, books := dotFolderLibrary(t)
	for title, b := range books {
		p := filepath.Join(libraryDir, b[1])
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEpubAt(t, p, title, b[0], "")
	}
	s.ScanLibrary(ctx)
	if b := readUnmatchedBlob(t, ctx, settings); b.FilesFound != len(books) {
		t.Errorf("files_found = %d, want %d: a book in a dot-named folder was not scanned", b.FilesFound, len(books))
	}
}

// Wiring: the snapshot behind the "already in the library" check leaves the
// scanner's download folders out. Dropping WithExcluded in SnapshotFinder, or
// the download folder from excludedLibraryDirs, makes the copy count as owned.
func TestSnapshotFinder_LeavesDownloadFoldersOut(t *testing.T) {
	s, _, _, _, _, libraryDir, _ := unmatchedFixture(t)
	author := filepath.Join(libraryDir, "Kari Nordmann")
	ebookDL := filepath.Join(author, "incoming")
	audioDL := filepath.Join(author, "incoming-audio")
	for _, dir := range []string{ebookDL, audioDL, filepath.Join(author, "Kept")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeEpubAt(t, filepath.Join(ebookDL, "Fjellvinden.epub"), "Fjellvinden", "Kari Nordmann", "")
	writeEpubAt(t, filepath.Join(audioDL, "Havgapet.epub"), "Havgapet", "Kari Nordmann", "")
	writeEpubAt(t, filepath.Join(author, "Kept", "Skogens sang.epub"), "Skogens sang", "Kari Nordmann", "")
	s.WithDownloadDir(ebookDL).WithAudiobookDownloadDir(audioDL)

	snap := s.SnapshotFinder()
	ctx := context.Background()
	for _, title := range []string{"Fjellvinden", "Havgapet"} {
		if got := snap.FindExisting(ctx, title, "Kari Nordmann", models.MediaTypeEbook); got != "" {
			t.Errorf("FindExisting(%q) = %q, want nothing: it exists only in a download folder", title, got)
		}
	}
	if got := snap.FindExisting(ctx, "Skogens sang", "Kari Nordmann", models.MediaTypeEbook); got == "" {
		t.Error("the library copy was not found")
	}
}

// Wiring: the walk resolves each folder it meets, so a download folder named
// by its real path is recognised under a library root that is itself a
// symlink (the walk then reports paths under the link). Dropping that
// EvalSymlinks check in libraryDirSkipper walks the download folder.
func TestWalkRoot_RealPathExclusionUnderSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	downloads := filepath.Join(real, "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, filepath.Join(downloads, "Fjellvinden.epub"), "Fjellvinden", "Kari Nordmann", "")
	if err := os.MkdirAll(filepath.Join(real, "Kari Nordmann"), 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(real, "Kari Nordmann", "Havgapet.epub")
	writeEpubAt(t, kept, "Havgapet", "Kari Nordmann", "")
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "library")
	symlinkOrSkip(t, real, link)

	var walked []string
	_ = walkRoot(link, []string{filepath.Join(resolvedReal, "downloads")}, func(path string, info os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(path, ".epub") {
			walked = append(walked, path)
		}
		return nil
	})
	if len(walked) != 1 || walked[0] != filepath.Join(link, "Kari Nordmann", "Havgapet.epub") {
		t.Errorf("walked %v, want only the book outside the download folder", walked)
	}
}
