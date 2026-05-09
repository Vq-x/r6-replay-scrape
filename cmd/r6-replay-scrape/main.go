package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/openclaw/r6-replay-scrape/internal/scraper"
)

func main() {
	cfg := scraper.DefaultConfig()

	flag.IntVar(&cfg.StartMatchID, "start-id", cfg.StartMatchID, "first Ubisoft match id to check when no progress file exists")
	flag.IntVar(&cfg.MaxMatches, "max-matches", 0, "maximum match pages to check before exiting; 0 means run until stop thresholds")
	flag.IntVar(&cfg.PageConcurrency, "page-concurrency", cfg.PageConcurrency, "concurrent match page requests")
	flag.IntVar(&cfg.DownloadConcurrency, "download-concurrency", cfg.DownloadConcurrency, "concurrent replay downloads")
	flag.IntVar(&cfg.MaxConsecutive404s, "max-404", cfg.MaxConsecutive404s, "stop after this many consecutive 404 match pages")
	flag.IntVar(&cfg.MaxPage500Errors, "max-500", cfg.MaxPage500Errors, "stop match indexing after this many HTTP 500 page failures")
	flag.IntVar(&cfg.Max429Retries, "max-429-retries", cfg.Max429Retries, "maximum retries for HTTP 429 responses and interrupted downloads")
	flag.StringVar(&cfg.OutputDir, "output-dir", cfg.OutputDir, "directory for downloaded replay archives")
	flag.StringVar(&cfg.ProgressPath, "progress", cfg.ProgressPath, "JSON progress file path")
	flag.BoolVar(&cfg.NoDownload, "no-download", false, "discover replay links and update progress without downloading archives")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := scraper.NewRunner(cfg).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "r6-replay-scrape: %v\n", err)
		os.Exit(1)
	}
}
