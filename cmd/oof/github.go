package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

func postGitHubComment(token, repo string, prNumber int, body string) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`).MatchString(repo) {
		return fmt.Errorf("invalid repository format %q: must be owner/name", repo)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, prNumber)

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body) // drain to allow connection reuse
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	return nil
}
