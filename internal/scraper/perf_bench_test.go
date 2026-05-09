package scraper

import "testing"

var benchReplayHTML = `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"match":{"replayLink":"https://cdn.example/replays/match%201.zip"}}}}</script></html>`

func BenchmarkExtractReplayLink(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = ExtractReplayLink(benchReplayHTML)
	}
}

func BenchmarkFilenameFromURL(b *testing.B) {
	raw := "https://cdn.example/replays/R6%20Replay.zip?token=abc"
	for i := 0; i < b.N; i++ {
		_, _ = FilenameFromURL(raw)
	}
}

func BenchmarkFormatBytes(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = FormatBytes(123456789)
	}
}
