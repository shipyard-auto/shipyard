package crewctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// crewReleasesAPI lists recent releases ordered newest-first. 30 entries is
// enough to skip intermixed core/fairway tags without paginating.
const crewReleasesAPI = "https://api.github.com/repos/shipyard-auto/shipyard/releases?per_page=30"

type releaseListItem struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// ErrNoCrewRelease is returned when no stable crew-v* release is visible in
// the first page of GitHub releases.
var ErrNoCrewRelease = errors.New("crew: no stable release found")

// ResolveLatestCrewVersion fetches the newest non-draft, non-prerelease crew
// release version from GitHub. The tag pattern is `crew-v<semver>`; other
// release lines (core, fairway) are ignored.
func ResolveLatestCrewVersion(ctx context.Context, client HTTPClient) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, crewReleasesAPI, nil)
	if err != nil {
		return "", fmt.Errorf("crew: create version request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("crew: request latest version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("crew: request latest version: unexpected status %s", resp.Status)
	}

	var releases []releaseListItem
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", fmt.Errorf("crew: decode version response: %w", err)
	}

	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		if strings.HasPrefix(r.TagName, "crew-v") {
			version := strings.TrimPrefix(r.TagName, "crew-v")
			if version != "" {
				return version, nil
			}
		}
	}

	return "", ErrNoCrewRelease
}
