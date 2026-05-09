package scraper

import "testing"

func TestExtractReplayLinkFromNextData(t *testing.T) {
	html := `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"match":{"replayLink":"https://cdn.example/replays/match%201.zip"}}}}</script></html>`

	got, err := ExtractReplayLink(html)
	if err != nil {
		t.Fatalf("ExtractReplayLink returned error: %v", err)
	}
	want := "https://cdn.example/replays/match%201.zip"
	if got != want {
		t.Fatalf("ExtractReplayLink = %q, want %q", got, want)
	}
}

func TestExtractReplayLinkFallbackUnescapesJSONString(t *testing.T) {
	html := `<html>{"replayLink":"https:\/\/cdn.example\/replays\/match.zip?token=a\u0026b"}</html>`

	got, err := ExtractReplayLink(html)
	if err != nil {
		t.Fatalf("ExtractReplayLink returned error: %v", err)
	}
	want := "https://cdn.example/replays/match.zip?token=a&b"
	if got != want {
		t.Fatalf("ExtractReplayLink = %q, want %q", got, want)
	}
}

func TestExtractReplayLinkMissing(t *testing.T) {
	if _, err := ExtractReplayLink("<html></html>"); err == nil {
		t.Fatal("ExtractReplayLink returned nil error for missing replay link")
	}
}

func TestFilenameFromURL(t *testing.T) {
	got, err := FilenameFromURL("https://cdn.example/replays/R6%20Replay.zip?token=abc")
	if err != nil {
		t.Fatalf("FilenameFromURL returned error: %v", err)
	}
	want := "R6 Replay.zip"
	if got != want {
		t.Fatalf("FilenameFromURL = %q, want %q", got, want)
	}
}

func TestFilenameFromURLRejectsEmptyPath(t *testing.T) {
	if _, err := FilenameFromURL("https://cdn.example/"); err == nil {
		t.Fatal("FilenameFromURL returned nil error for empty filename")
	}
}

func TestFindKeyWalksNestedData(t *testing.T) {
	data := map[string]any{
		"outer": []any{
			map[string]any{"ignored": ""},
			map[string]any{"inner": map[string]any{"replayLink": "https://example/replay.zip"}},
		},
	}

	got, ok := FindKey(data, "replayLink")
	if !ok {
		t.Fatal("FindKey did not find nested replayLink")
	}
	if got != "https://example/replay.zip" {
		t.Fatalf("FindKey = %v", got)
	}
}
