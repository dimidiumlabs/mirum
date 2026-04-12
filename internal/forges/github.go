// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package forges

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// GitHub implements the Forge interface for GitHub and compatible APIs.
type GitHub struct {
	Secret string
	Token  string
}

var githubStatusMap = map[Status]string{
	StatusPending: "pending",
	StatusRunning: "pending", // GitHub has no "running" state
	StatusSuccess: "success",
	StatusFailure: "failure",
}

type githubPush struct {
	Ref   string `json:"ref"`
	After string `json:"after"`
	Repo  struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func (g *GitHub) Webhook(r *http.Request, body []byte) (*PushEvent, error) {
	if g.Secret != "" && !g.verifySignature(body, r.Header.Get("X-Hub-Signature-256")) {
		return nil, ErrInvalidSignature
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "push" {
		return nil, nil
	}

	var push githubPush
	if err := json.Unmarshal(body, &push); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}

	if push.After == "" || push.After == "0000000000000000000000000000000000000000" {
		return nil, nil
	}

	if !strings.HasPrefix(push.Ref, "refs/heads/") {
		return nil, nil
	}

	owner, repo := splitFullName(push.Repo.FullName)
	return &PushEvent{
		Owner:    owner,
		Repo:     repo,
		Branch:   strings.TrimPrefix(push.Ref, "refs/heads/"),
		SHA:      push.After,
		CloneURL: push.Repo.CloneURL,
	}, nil
}

func (g *GitHub) SetStatus(ctx context.Context, ev *PushEvent, status Status, desc string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/statuses/%s", ev.Owner, ev.Repo, ev.SHA)

	body, _ := json.Marshal(map[string]string{
		"state":       githubStatusMap[status],
		"description": desc,
		"context":     "mirum",
	})

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("github: close response body", "err", err)
		}
	}()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %d: %s", resp.StatusCode, b)
	}
	return nil
}

func (g *GitHub) AuthURL(cloneURL string) string {
	if g.Token == "" {
		return cloneURL
	}
	u, err := url.Parse(cloneURL)
	if err != nil {
		return cloneURL
	}
	u.User = url.UserPassword("x-access-token", g.Token)
	return u.String()
}

func (g *GitHub) verifySignature(payload []byte, signature string) bool {
	sig, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	decoded, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(g.Secret))
	mac.Write(payload)
	return hmac.Equal(mac.Sum(nil), decoded)
}

func splitFullName(fullName string) (string, string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return fullName, ""
	}
	return parts[0], parts[1]
}
