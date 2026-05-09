package scraper

import (
	"path/filepath"
	"testing"
)

func TestProgressRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	progress := NewProgress()
	progress.RecordMatch(BaseMatchURL, 6000, "replay_found", "https://cdn.example/replay.zip", "")
	progress.RecordDownload("https://cdn.example/replay.zip", "downloaded", "replays/replay.zip", "")

	if err := SaveProgress(path, progress); err != nil {
		t.Fatalf("SaveProgress returned error: %v", err)
	}

	loaded, err := LoadProgress(path)
	if err != nil {
		t.Fatalf("LoadProgress returned error: %v", err)
	}
	if loaded.NextMatchID != 6001 {
		t.Fatalf("NextMatchID = %d, want 6001", loaded.NextMatchID)
	}
	if loaded.Counts.ReplayLinksFound != 1 {
		t.Fatalf("ReplayLinksFound = %d, want 1", loaded.Counts.ReplayLinksFound)
	}
	if loaded.Counts.DownloadsSucceeded != 1 {
		t.Fatalf("DownloadsSucceeded = %d, want 1", loaded.Counts.DownloadsSucceeded)
	}
	if loaded.Counts.UniqueReplayLinks != 1 {
		t.Fatalf("UniqueReplayLinks = %d, want 1", loaded.Counts.UniqueReplayLinks)
	}
}
