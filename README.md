# R6 Replay Scrape

Utilities for finding and downloading Rainbow Six Siege esports replay archives
from Ubisoft match pages.

The original Python scraper remains in `replayScrape.py`. A Go CLI is also
available for the same workflow with resumable progress tracking and bounded
validation options.

## Go CLI

Build or run the Go scraper from the repository root:

```sh
go run ./cmd/r6-replay-scrape
```

By default the CLI:

- starts at match ID `6000`, or resumes from `replay_progress.json`
- scans Ubisoft match pages concurrently
- extracts `replayLink` from the page's Next.js data or raw HTML
- records progress in `replay_progress.json`
- downloads each unique replay archive into `replays/`
- stops after `1000` consecutive 404 pages or `50` HTTP 500 page failures

Useful bounded runs:

```sh
go run ./cmd/r6-replay-scrape -max-matches 25 -no-download
go run ./cmd/r6-replay-scrape -progress /tmp/r6-progress.json -output-dir /tmp/r6-replays -max-matches 10
```

Common flags:

```text
-start-id int              first match id when no progress file exists
-max-matches int           maximum pages to check; 0 runs until stop thresholds
-page-concurrency int      concurrent match page requests
-download-concurrency int  concurrent replay downloads
-progress string           JSON progress file path
-output-dir string         replay archive output directory
-no-download               discover replay links without downloading archives
```

Avoid committing replay archives or generated progress files. For validation,
prefer `/tmp` paths and `-max-matches`/`-no-download`.

## Python Scraper

The Python implementation can still be run directly:

```sh
uv run python replayScrape.py
```

It uses `aiohttp` and BeautifulSoup, writes `replay_progress.json`, and
downloads archives to `replays/`.

## Tests

```sh
go test ./...
```
