package scraper

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const StartMatchID = 6000

type Counts struct {
	MatchesProcessed     int `json:"matchesProcessed"`
	Matches404           int `json:"matches404"`
	PageFailures         int `json:"pageFailures"`
	Page500Failures      int `json:"page500Failures"`
	MatchesWithoutReplay int `json:"matchesWithoutReplay"`
	ReplayLinksFound     int `json:"replayLinksFound"`
	UniqueReplayLinks    int `json:"uniqueReplayLinks"`
	DownloadsSucceeded   int `json:"downloadsSucceeded"`
	DownloadsFailed      int `json:"downloadsFailed"`
}

type MatchProgress struct {
	MatchID    int    `json:"matchId"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	CheckedAt  string `json:"checkedAt"`
	ReplayLink string `json:"replayLink,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ReplayProgress struct {
	URL          string `json:"url"`
	FirstMatchID *int   `json:"firstMatchId"`
	MatchIDs     []int  `json:"matchIds"`
	Status       string `json:"status"`
	FoundAt      string `json:"foundAt"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
	OutputPath   string `json:"outputPath,omitempty"`
	Error        string `json:"error,omitempty"`
}

type Progress struct {
	StartedAt            string                     `json:"startedAt"`
	UpdatedAt            string                     `json:"updatedAt"`
	NextMatchID          int                        `json:"nextMatchId"`
	LastProcessedMatchID *int                       `json:"lastProcessedMatchId"`
	Consecutive404s      int                        `json:"consecutive404s"`
	Counts               Counts                     `json:"counts"`
	Matches              map[string]MatchProgress   `json:"matches"`
	ReplayLinks          map[string]*ReplayProgress `json:"replayLinks"`
}

func UTCNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func NewProgress() *Progress {
	now := UTCNow()
	return &Progress{
		StartedAt:       now,
		UpdatedAt:       now,
		NextMatchID:     StartMatchID,
		Matches:         map[string]MatchProgress{},
		ReplayLinks:     map[string]*ReplayProgress{},
		Consecutive404s: 0,
	}
}

func LoadProgress(progressPath string) (*Progress, error) {
	data, err := os.ReadFile(progressPath)
	if errors.Is(err, os.ErrNotExist) {
		return NewProgress(), nil
	}
	if err != nil {
		return nil, err
	}

	var progress Progress
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, err
	}

	if progress.StartedAt == "" {
		progress.StartedAt = UTCNow()
	}
	if progress.UpdatedAt == "" {
		progress.UpdatedAt = UTCNow()
	}
	if progress.NextMatchID == 0 {
		progress.NextMatchID = StartMatchID
	}
	if progress.Matches == nil {
		progress.Matches = map[string]MatchProgress{}
	}
	if progress.ReplayLinks == nil {
		progress.ReplayLinks = map[string]*ReplayProgress{}
	}
	progress.Counts.UniqueReplayLinks = len(progress.ReplayLinks)

	return &progress, nil
}

func SaveProgress(progressPath string, progress *Progress) error {
	progress.UpdatedAt = UTCNow()
	if err := os.MkdirAll(filepath.Dir(progressPath), 0o755); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(filepath.Dir(progressPath), filepath.Base(progressPath)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	encoder := json.NewEncoder(tempFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(progress); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	return os.Rename(tempPath, progressPath)
}

func (p *Progress) RecordMatch(baseMatchURL string, matchID int, status string, replayLink string, errText string) {
	checkedAt := UTCNow()
	p.Matches[itoa(matchID)] = MatchProgress{
		MatchID:    matchID,
		URL:        baseMatchURL + "/" + itoa(matchID),
		Status:     status,
		CheckedAt:  checkedAt,
		ReplayLink: replayLink,
		Error:      errText,
	}

	if p.LastProcessedMatchID == nil || matchID > *p.LastProcessedMatchID {
		id := matchID
		p.LastProcessedMatchID = &id
	}
	if p.NextMatchID < matchID+1 {
		p.NextMatchID = matchID + 1
	}
	p.Counts.MatchesProcessed++

	switch status {
	case "404":
		p.Counts.Matches404++
	case "page_failed":
		p.Counts.PageFailures++
	case "page_500":
		p.Counts.PageFailures++
		p.Counts.Page500Failures++
	case "no_replay":
		p.Counts.MatchesWithoutReplay++
	case "replay_found":
		p.Counts.ReplayLinksFound++
		p.ensureReplay(replayLink, matchID)
	}
}

func (p *Progress) RecordDownload(replayLink string, status string, outputPath string, errText string) {
	replay := p.ensureReplay(replayLink, 0)
	replay.Status = status
	replay.UpdatedAt = UTCNow()
	if outputPath != "" {
		replay.OutputPath = outputPath
	}
	if errText != "" {
		replay.Error = errText
	}

	switch status {
	case "downloaded":
		p.Counts.DownloadsSucceeded++
	case "download_failed":
		p.Counts.DownloadsFailed++
	}
	p.Counts.UniqueReplayLinks = len(p.ReplayLinks)
}

func (p *Progress) ensureReplay(replayLink string, matchID int) *ReplayProgress {
	replay, ok := p.ReplayLinks[replayLink]
	if !ok {
		var first *int
		if matchID != 0 {
			id := matchID
			first = &id
		}
		replay = &ReplayProgress{
			URL:          replayLink,
			FirstMatchID: first,
			Status:       "found",
			FoundAt:      UTCNow(),
			MatchIDs:     []int{},
		}
		p.ReplayLinks[replayLink] = replay
	}

	if matchID != 0 && !containsInt(replay.MatchIDs, matchID) {
		replay.MatchIDs = append(replay.MatchIDs, matchID)
		if replay.FirstMatchID == nil {
			id := matchID
			replay.FirstMatchID = &id
		}
	}
	p.Counts.UniqueReplayLinks = len(p.ReplayLinks)
	return replay
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
