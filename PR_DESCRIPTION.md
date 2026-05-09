# Port R6 replay scraper to Go

## Summary

This PR adds a Go CLI port of the existing Rainbow Six Siege replay scraper while keeping the original Python implementation available in `replayScrape.py`.

The new `cmd/r6-replay-scrape` CLI preserves the existing workflow—scan Ubisoft match pages, extract `replayLink` values, persist resumable progress, and download unique replay archives—while adding typed configuration, concurrent workers, bounded validation flags, and explicit retry/stop thresholds.

## What changed

- Added a Go module and `cmd/r6-replay-scrape` entrypoint.
- Implemented concurrent match-page scanning with configurable page and download worker counts.
- Extracted replay links from both Next.js `__NEXT_DATA__` JSON and raw HTML fallback data.
- Added resumable JSON progress tracking for checked matches, replay links, download status, counters, and next match ID.
- Added download handling for unique replay archives, including output filename derivation from replay URLs.
- Added retry/backoff handling for rate limits and interrupted downloads, plus configurable stop thresholds for consecutive 404s and HTTP 500 page failures.
- Added `-max-matches` and `-no-download` flags so reviewers can run small, non-destructive validation passes.
- Updated the README with Go CLI usage, common flags, and validation guidance while preserving Python scraper instructions.
- Added Go unit tests and benchmarks for parser/progress hot paths.

## Validation

```sh
gofmt -w cmd/r6-replay-scrape/main.go internal/scraper/*.go
go test ./...
go run ./cmd/r6-replay-scrape -h
```

A bounded live discovery check can be run without downloading replay archives:

```sh
go run ./cmd/r6-replay-scrape \
  -max-matches 5 \
  -no-download \
  -progress /tmp/r6-progress.json \
  -output-dir /tmp/r6-replays
```

Generated replay archives and progress files should stay out of the repository; prefer `/tmp` or another ignored path for validation runs.

## Performance notes

Local CPU microbenchmarks show the Go implementation is faster on scraper hot paths:

- Replay-link extraction: ~18.9x faster than the Python equivalent.
- Filename derivation from replay URLs: ~4.6x faster.
- Byte-formatting helper: ~2.1x faster.

These benchmarks use synthetic local inputs and no network or CDN downloads. Real end-to-end scrape time remains mostly network-bound, but the Go port reduces local parsing/progress overhead and removes the Python runtime/dependency setup from the main scraper workflow.

## Compatibility and review notes

- The Python scraper is intentionally left in place for comparison and fallback.
- The Go progress file uses the same default path, `replay_progress.json`, and records enough state to resume indexing and downloads.
- The default run remains open-ended until stop thresholds are reached; reviewers should use `-max-matches` for bounded checks.
