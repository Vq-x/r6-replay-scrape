# PR Notes

Suggested title: Port R6 replay scraper to Go

## Summary

- Added a Go module and `cmd/r6-replay-scrape` CLI.
- Implemented Python-equivalent match scanning, replay link extraction, resumable progress JSON, retry/backoff handling, and replay downloads.
- Preserved `replayScrape.py` as the original Python implementation.
- Documented Go and Python usage in `README.md`.
- Added Go tests for replay-link parsing, URL filename derivation, nested key lookup, and progress round-tripping.

## Tests And Validation

- Ran `gofmt` across Go files.
- Ran `GOCACHE=/tmp/go-build-cache go test ./...`.
- Ran `GOCACHE=/tmp/go-build-cache go run ./cmd/r6-replay-scrape -h`.
- A plain `go test ./...` failed before compilation because this sandbox's default Go build cache under `/home/vqx/.cache/go-build` is read-only.
- Network validation should use bounded commands such as:

```sh
go run ./cmd/r6-replay-scrape -max-matches 5 -no-download -progress /tmp/r6-progress.json -output-dir /tmp/r6-replays
```

## Blockers

- Worker sandbox could not write `/home/vqx/openclaw-workspace/research/r6-replay-scrape-go-port-progress.md`; the main session wrote it afterward.
- Worker sandbox could not create the nested `feature/go-port` branch or commit, so the main session created `feature-go-port-replay` and handled commit/cleanup.

## Suggested PR Body

This PR adds a Go CLI port of the existing Python R6 replay scraper while keeping the Python script intact. The Go implementation resumes from the same style of progress JSON, scans Ubisoft match pages concurrently, extracts replay links from Next.js data or raw HTML, queues unique downloads, and supports bounded validation with `-max-matches` and `-no-download`.

Tests cover pure parsing, URL filename extraction, recursive key lookup, and progress file persistence. Large replay archives are not committed; validation should keep generated artifacts under `/tmp` or another ignored location.
