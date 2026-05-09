# PR Notes

Suggested title: Port R6 replay scraper to Go

Primary PR body draft: [`PR_DESCRIPTION.md`](./PR_DESCRIPTION.md)

## Upstream-ready summary

This PR adds a Go CLI port of the existing R6 replay scraper while keeping `replayScrape.py` intact. The Go implementation scans Ubisoft match pages concurrently, extracts `replayLink` values from Next.js data or raw HTML fallback content, writes resumable JSON progress, downloads unique replay archives, and exposes bounded validation flags for safer review runs.

## Highlights

- Added Go module and `cmd/r6-replay-scrape` CLI.
- Implemented Python-equivalent match scanning, replay-link extraction, progress persistence, retry/backoff behavior, stop thresholds, and replay downloads.
- Added configurable page/download concurrency plus `-max-matches` and `-no-download` for bounded validation.
- Preserved the original Python scraper for comparison/fallback.
- Updated `README.md` with Go and Python usage, common flags, and validation guidance.
- Added Go tests for replay-link parsing, URL filename derivation, nested key lookup, and progress round-tripping.
- Added benchmarks for parser/progress helper hot paths.

## Tests and validation

Validated in this workspace:

```sh
go test ./...
```

Previously validated while preparing the branch:

```sh
gofmt -w cmd/r6-replay-scrape/main.go internal/scraper/*.go
GOCACHE=/tmp/go-build-cache go test ./...
GOCACHE=/tmp/go-build-cache go run ./cmd/r6-replay-scrape -h
```

Suggested bounded reviewer check:

```sh
go run ./cmd/r6-replay-scrape \
  -max-matches 5 \
  -no-download \
  -progress /tmp/r6-progress.json \
  -output-dir /tmp/r6-replays
```

Avoid committing generated replay archives or progress files; use `/tmp` or another ignored path for validation.

## Performance

Local CPU benchmarks against equivalent Python hot paths show the Go port is faster:

- Extract `replayLink`: ~18.9x faster.
- Filename from URL: ~4.6x faster.
- Byte formatting helper: ~2.1x faster.

Detailed report: `/home/vqx/openclaw-workspace/research/r6-scraper-go-port-performance-report.md`.

Caveat for PR wording: these are synthetic local microbenchmarks with no live Ubisoft requests or archive downloads. Real scrape time is still mostly network/CDN-bound.

## Internal handoff notes

- Branch: `feature-go-port-replay`
- Current head: `f029ead` (`test: add replay scraper benchmarks`)
- Do not include worker/sandbox blockers in the upstream PR body; they are not relevant to reviewers.
