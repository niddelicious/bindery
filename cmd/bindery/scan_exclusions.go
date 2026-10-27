package main

import (
	"log/slog"

	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/importer"
)

// wireScanDownloadDirs tells the importer where the download folders are, so
// library walks (the scan and the "already in the library" check) leave them
// out when a torrent client saves inside a library root (#3135). Split out of
// main so a test can lock the wiring in.
func wireScanDownloadDirs(sc *importer.Scanner, cfg *config.Config) {
	sc.WithDownloadDir(cfg.DownloadDir)
	if cfg.AudiobookDownloadDir != "" {
		sc.WithAudiobookDownloadDir(cfg.AudiobookDownloadDir)
		slog.Info("audiobook download dir configured", "path", cfg.AudiobookDownloadDir)
	}
}

// manualImportExcludedDirs is what folder import leaves out below the folder
// being imported: the same download folders the library scan leaves out.
func manualImportExcludedDirs(cfg *config.Config) []string {
	return []string{cfg.DownloadDir, cfg.AudiobookDownloadDir}
}
