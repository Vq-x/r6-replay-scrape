package scraper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errRetryAlreadyDelayed = errors.New("retry already delayed")

const (
	BaseMatchURL               = "https://www.ubisoft.com/en-us/esports/rainbow-six/siege/match"
	DefaultProgressPath        = "replay_progress.json"
	DefaultOutputDir           = "replays"
	DefaultPageConcurrency     = 8
	DefaultDownloadConcurrency = 3
	DefaultMaxConsecutive404s  = 1000
	DefaultMaxPage500Errors    = 50
	DefaultMax429Retries       = 6
	DefaultSaveInterval        = 5 * time.Second
	chunkSize                  = 1024 * 1024
	downloadLogIntervalBytes   = 64 * 1024 * 1024
	backoffBase                = time.Second
	backoffMax                 = time.Minute
)

type Config struct {
	StartMatchID        int
	MaxMatches          int
	PageConcurrency     int
	DownloadConcurrency int
	MaxConsecutive404s  int
	MaxPage500Errors    int
	Max429Retries       int
	OutputDir           string
	ProgressPath        string
	NoDownload          bool
	SaveInterval        time.Duration
}

type Runner struct {
	cfg      Config
	client   *http.Client
	progress *Progress
	mu       sync.Mutex
	dirty    bool
}

type matchResult struct {
	matchID    int
	replayLink string
	status     string
	errText    string
}

type downloadJob struct {
	matchID    int
	replayLink string
}

func DefaultConfig() Config {
	return Config{
		StartMatchID:        StartMatchID,
		PageConcurrency:     DefaultPageConcurrency,
		DownloadConcurrency: DefaultDownloadConcurrency,
		MaxConsecutive404s:  DefaultMaxConsecutive404s,
		MaxPage500Errors:    DefaultMaxPage500Errors,
		Max429Retries:       DefaultMax429Retries,
		OutputDir:           DefaultOutputDir,
		ProgressPath:        DefaultProgressPath,
		SaveInterval:        DefaultSaveInterval,
	}
}

func NewRunner(cfg Config) *Runner {
	if cfg.StartMatchID == 0 {
		cfg.StartMatchID = StartMatchID
	}
	if cfg.PageConcurrency <= 0 {
		cfg.PageConcurrency = DefaultPageConcurrency
	}
	if cfg.DownloadConcurrency <= 0 {
		cfg.DownloadConcurrency = DefaultDownloadConcurrency
	}
	if cfg.MaxConsecutive404s <= 0 {
		cfg.MaxConsecutive404s = DefaultMaxConsecutive404s
	}
	if cfg.MaxPage500Errors <= 0 {
		cfg.MaxPage500Errors = DefaultMaxPage500Errors
	}
	if cfg.Max429Retries <= 0 {
		cfg.Max429Retries = DefaultMax429Retries
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = DefaultOutputDir
	}
	if cfg.ProgressPath == "" {
		cfg.ProgressPath = DefaultProgressPath
	}
	if cfg.SaveInterval <= 0 {
		cfg.SaveInterval = DefaultSaveInterval
	}

	return &Runner{
		cfg: cfg,
		client: &http.Client{
			Timeout: 0,
			Transport: headerTransport{
				base: http.DefaultTransport,
			},
		},
	}
}

func (r *Runner) Run(ctx context.Context) error {
	progress, err := LoadProgress(r.cfg.ProgressPath)
	if err != nil {
		return err
	}
	r.progress = progress
	if err := r.save(); err != nil {
		return err
	}
	fmt.Printf("Saving progress to: %s\n", r.cfg.ProgressPath)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopSaver := make(chan struct{})
	var saverWG sync.WaitGroup
	saverWG.Add(1)
	go r.periodicSave(stopSaver, &saverWG)
	defer func() {
		close(stopSaver)
		saverWG.Wait()
		_ = r.save()
	}()

	matchJobs := make(chan int)
	matchResults := make(chan matchResult, r.cfg.PageConcurrency)
	var pageWG sync.WaitGroup
	for workerID := 0; workerID < r.cfg.PageConcurrency; workerID++ {
		pageWG.Add(1)
		go func() {
			defer pageWG.Done()
			for matchID := range matchJobs {
				matchResults <- r.scrapeMatch(ctx, matchID)
			}
		}()
	}
	go func() {
		pageWG.Wait()
		close(matchResults)
	}()

	downloadJobs := make(chan downloadJob, r.cfg.DownloadConcurrency*4)
	var downloadWG sync.WaitGroup
	scheduledDownloads := map[string]struct{}{}
	if !r.cfg.NoDownload {
		for workerID := 1; workerID <= r.cfg.DownloadConcurrency; workerID++ {
			downloadWG.Add(1)
			go r.downloadWorker(ctx, "download-"+strconv.Itoa(workerID), downloadJobs, &downloadWG)
		}
		r.queuePendingDownloads(downloadJobs, scheduledDownloads)
	}

	nextMatchID := maxInt(r.cfg.StartMatchID, progress.NextMatchID)
	consecutive404s := progress.Consecutive404s
	page500Errors := 0
	active := 0
	scheduledMatches := 0
	stopScheduling := false

	schedule := func() bool {
		if stopScheduling || (r.cfg.MaxMatches > 0 && scheduledMatches >= r.cfg.MaxMatches) {
			return false
		}
		fmt.Printf("Checking match %d\n", nextMatchID)
		matchJobs <- nextMatchID
		nextMatchID++
		active++
		scheduledMatches++
		return true
	}

	for active < r.cfg.PageConcurrency && schedule() {
	}

	for active > 0 {
		result, ok := <-matchResults
		if !ok {
			break
		}
		active--

		switch result.status {
		case "404":
			consecutive404s++
			r.mu.Lock()
			r.progress.Consecutive404s = consecutive404s
			r.progress.RecordMatch(BaseMatchURL, result.matchID, "404", "", "")
			r.markDirtyLocked()
			r.mu.Unlock()
			fmt.Printf("Match %d: 404 received (%d/%d)\n", result.matchID, consecutive404s, r.cfg.MaxConsecutive404s)
			if consecutive404s >= r.cfg.MaxConsecutive404s {
				fmt.Printf("Stopping after %d consecutive 404s\n", r.cfg.MaxConsecutive404s)
				stopScheduling = true
			}
		case "page_500":
			page500Errors++
			r.recordMatch(result)
			fmt.Printf("Match %d: HTTP 500 received (%d/%d)\n", result.matchID, page500Errors, r.cfg.MaxPage500Errors)
			if page500Errors >= r.cfg.MaxPage500Errors {
				fmt.Printf("Stopping match indexing after %d HTTP 500 errors\n", r.cfg.MaxPage500Errors)
				stopScheduling = true
			}
		default:
			if result.status != "page_failed" {
				consecutive404s = 0
				r.mu.Lock()
				r.progress.Consecutive404s = 0
				r.markDirtyLocked()
				r.mu.Unlock()
			}
			r.recordMatch(result)
			if result.status == "replay_found" && !r.cfg.NoDownload {
				if _, exists := scheduledDownloads[result.replayLink]; !exists {
					scheduledDownloads[result.replayLink] = struct{}{}
					downloadJobs <- downloadJob{matchID: result.matchID, replayLink: result.replayLink}
					fmt.Printf("Match %d: queued replay download\n", result.matchID)
				}
			}
		}

		if !schedule() && active == 0 {
			stopScheduling = true
		}
	}

	close(matchJobs)
	if !r.cfg.NoDownload {
		close(downloadJobs)
		downloadWG.Wait()
	}

	return r.save()
}

func (r *Runner) scrapeMatch(ctx context.Context, matchID int) matchResult {
	matchURL := BaseMatchURL + "/" + strconv.Itoa(matchID)
	html, status, err := r.fetchMatchHTML(ctx, matchURL)
	if err != nil {
		if status == http.StatusInternalServerError {
			fmt.Printf("Match %d: HTTP 500 page request failed\n", matchID)
			return matchResult{matchID: matchID, status: "page_500", errText: err.Error()}
		}
		fmt.Printf("Match %d: page request failed, continuing: %v\n", matchID, err)
		return matchResult{matchID: matchID, status: "page_failed", errText: err.Error()}
	}
	if status == http.StatusNotFound {
		return matchResult{matchID: matchID, status: "404"}
	}

	replayLink, err := ExtractReplayLink(html)
	if err != nil {
		fmt.Printf("Match %d: no replay link found\n", matchID)
		return matchResult{matchID: matchID, status: "no_replay"}
	}

	fmt.Printf("Match %d: found replay link: %s\n", matchID, replayLink)
	return matchResult{matchID: matchID, status: "replay_found", replayLink: replayLink}
}

func (r *Runner) fetchMatchHTML(ctx context.Context, matchURL string) (string, int, error) {
	var lastErr error
	for attempt := 1; attempt <= r.cfg.Max429Retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, matchURL, nil)
		if err != nil {
			return "", 0, err
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return "", 0, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return "", resp.StatusCode, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < r.cfg.Max429Retries {
			r.sleepAfter429(ctx, matchURL, attempt, resp)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return "", resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}
		return string(body), resp.StatusCode, nil
	}
	return "", 0, fmt.Errorf("%s: exceeded retry limit: %w", matchURL, lastErr)
}

func (r *Runner) downloadWorker(ctx context.Context, name string, jobs <-chan downloadJob, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobs {
		fmt.Printf("Match %d: downloading replay\n", job.matchID)
		outputPath, err := r.downloadFile(ctx, job.replayLink, "Match "+strconv.Itoa(job.matchID))
		if err != nil {
			fmt.Printf("Match %d: download failed, continuing: %v\n", job.matchID, err)
			r.recordDownload(job.replayLink, "download_failed", "", err.Error())
			continue
		}
		fmt.Printf("Match %d: downloaded replay to: %s\n", job.matchID, outputPath)
		r.recordDownload(job.replayLink, "downloaded", outputPath, "")
	}
	fmt.Printf("%s: stopped\n", name)
}

func (r *Runner) downloadFile(ctx context.Context, downloadURL string, label string) (string, error) {
	if err := os.MkdirAll(r.cfg.OutputDir, 0o755); err != nil {
		return "", err
	}
	filename, err := FilenameFromURL(downloadURL)
	if err != nil {
		return "", err
	}
	outputPath := filepath.Join(r.cfg.OutputDir, filename)
	tempPath := outputPath + ".part"

	if info, err := os.Stat(outputPath); err == nil && info.Size() > 0 {
		return outputPath, nil
	}

	var lastErr error
	for attempt := 1; attempt <= r.cfg.Max429Retries; attempt++ {
		err := r.downloadAttempt(ctx, downloadURL, outputPath, tempPath, label, attempt)
		if err == nil {
			return outputPath, nil
		}
		lastErr = err
		_ = os.Remove(tempPath)
		if attempt >= r.cfg.Max429Retries {
			break
		}
		if errors.Is(err, errRetryAlreadyDelayed) {
			continue
		}
		delay := retryDelay(attempt)
		fmt.Printf("%s: download interrupted (%v), retrying in %.1fs\n", downloadURL, err, delay.Seconds())
		if !sleepContext(ctx, delay) {
			return "", ctx.Err()
		}
	}

	return "", fmt.Errorf("%s: exceeded retry limit: %w", downloadURL, lastErr)
}

func (r *Runner) downloadAttempt(ctx context.Context, downloadURL, outputPath, tempPath, label string, attempt int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests && attempt < r.cfg.Max429Retries {
		r.sleepAfter429(ctx, downloadURL, attempt, resp)
		return errRetryAlreadyDelayed
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	file, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer file.Close()

	contentLength := resp.ContentLength
	if contentLength > 0 {
		fmt.Printf("%s: downloading %s to %s\n", label, FormatBytes(float64(contentLength)), filepath.Base(outputPath))
	}

	buffer := make([]byte, chunkSize)
	var downloaded int64
	nextLogAt := int64(downloadLogIntervalBytes)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			written, writeErr := file.Write(buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			downloaded += int64(written)
			if downloaded >= nextLogAt {
				if contentLength > 0 {
					percent := float64(downloaded) / float64(contentLength) * 100
					fmt.Printf("%s: %s / %s (%.1f%%)\n", label, FormatBytes(float64(downloaded)), FormatBytes(float64(contentLength)), percent)
				} else {
					fmt.Printf("%s: %s\n", label, FormatBytes(float64(downloaded)))
				}
				nextLogAt += downloadLogIntervalBytes
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, outputPath)
}

func (r *Runner) queuePendingDownloads(downloadJobs chan<- downloadJob, scheduled map[string]struct{}) {
	r.mu.Lock()
	pending := []downloadJob{}
	for replayLink, replay := range r.progress.ReplayLinks {
		if replay.Status == "downloaded" {
			continue
		}
		matchID := 0
		if replay.FirstMatchID != nil {
			matchID = *replay.FirstMatchID
		}
		scheduled[replayLink] = struct{}{}
		pending = append(pending, downloadJob{matchID: matchID, replayLink: replayLink})
	}
	r.mu.Unlock()

	for _, job := range pending {
		downloadJobs <- job
	}
	if len(pending) > 0 {
		fmt.Printf("Queued %d pending replay download(s) from progress\n", len(pending))
	}
}

func (r *Runner) recordMatch(result matchResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress.RecordMatch(BaseMatchURL, result.matchID, result.status, result.replayLink, result.errText)
	r.markDirtyLocked()
}

func (r *Runner) recordDownload(replayLink string, status string, outputPath string, errText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress.RecordDownload(replayLink, status, outputPath, errText)
	r.markDirtyLocked()
}

func (r *Runner) markDirtyLocked() {
	r.dirty = true
}

func (r *Runner) periodicSave(stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(r.cfg.SaveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = r.flushIfDirty()
		case <-stop:
			_ = r.flushIfDirty()
			return
		}
	}
}

func (r *Runner) flushIfDirty() error {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	r.dirty = false
	r.mu.Unlock()
	return r.save()
}

func (r *Runner) save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return SaveProgress(r.cfg.ProgressPath, r.progress)
}

func (r *Runner) sleepAfter429(ctx context.Context, label string, attempt int, resp *http.Response) {
	delay := retryAfter(resp.Header.Get("Retry-After"))
	if delay < 0 {
		delay = retryDelay(attempt)
	}
	fmt.Printf("%s: HTTP 429, retrying in %.1fs\n", label, delay.Seconds())
	sleepContext(ctx, delay)
}

func retryAfter(value string) time.Duration {
	if value == "" {
		return -1
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(maxInt(0, seconds)) * time.Second
	}
	if parsed, err := http.ParseTime(value); err == nil {
		delay := time.Until(parsed)
		if delay < 0 {
			return 0
		}
		return delay
	}
	return -1
}

func retryDelay(attempt int) time.Duration {
	delay := minDuration(backoffBase*time.Duration(1<<maxInt(0, attempt-1)), backoffMax)
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	return delay + jitter
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func FormatBytes(size float64) string {
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if size < 1024 || unit == "GB" {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
		size /= 1024
	}
	return fmt.Sprintf("%.1f GB", size)
}

type headerTransport struct {
	base http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:150.0) Gecko/20100101 Firefox/150.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	return t.base.RoundTrip(req)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
