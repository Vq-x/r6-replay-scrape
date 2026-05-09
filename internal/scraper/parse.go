package scraper

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	nextDataRe    = regexp.MustCompile(`(?is)<script\b[^>]*\bid=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
	replayLinkRe  = regexp.MustCompile(`"replayLink"\s*:\s*"((?:\\.|[^"\\])*)"`)
	spaceCollapse = regexp.MustCompile(`\s+`)
)

func FindKey(data any, key string) (any, bool) {
	switch value := data.(type) {
	case map[string]any:
		if found, ok := value[key]; ok && truthy(found) {
			return found, true
		}
		for _, item := range value {
			if found, ok := FindKey(item, key); ok {
				return found, true
			}
		}
	case []any:
		for _, item := range value {
			if found, ok := FindKey(item, key); ok {
				return found, true
			}
		}
	}

	return nil, false
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func ExtractReplayLink(pageHTML string) (string, error) {
	if match := nextDataRe.FindStringSubmatch(pageHTML); len(match) == 2 {
		script := strings.TrimSpace(html.UnescapeString(match[1]))
		var data any
		if err := json.Unmarshal([]byte(script), &data); err == nil {
			if found, ok := FindKey(data, "replayLink"); ok {
				if replayLink, ok := found.(string); ok && replayLink != "" {
					return replayLink, nil
				}
			}
		}
	}

	if match := replayLinkRe.FindStringSubmatch(pageHTML); len(match) == 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(`"`+match[1]+`"`), &unquoted); err != nil {
			return "", fmt.Errorf("decode replayLink: %w", err)
		}
		if unquoted != "" {
			return unquoted, nil
		}
	}

	return "", fmt.Errorf("could not find a replayLink in the HTML response")
}

func FilenameFromURL(downloadURL string) (string, error) {
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return "", err
	}

	filename, err := url.PathUnescape(path.Base(parsed.EscapedPath()))
	if err != nil {
		return "", err
	}
	filename = strings.TrimSpace(spaceCollapse.ReplaceAllString(filename, " "))
	if filename == "" || filename == "." || filename == "/" {
		return "", fmt.Errorf("could not determine a filename from %s", downloadURL)
	}

	return filename, nil
}
