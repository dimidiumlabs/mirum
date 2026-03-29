// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
)

var (
	addr   = flag.String("addr", ":3000", "listen address")
	secret = flag.String("secret", "", "GitHub webhook secret")
	token  = flag.String("token", "", "GitHub personal access token (required)")
	script = flag.String("script", ".mirum/main.star", "starlark script to run from repo root")
)

type pushEvent struct {
	Ref   string `json:"ref"`
	After string `json:"after"`
	Repo  struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func processPush(push pushEvent) {
	owner, repo := splitFullName(push.Repo.FullName)
	sha := push.After
	log := slog.With("repo", push.Repo.FullName, "sha", sha[:8])

	if err := setStatus(owner, repo, sha, "pending", "Build started"); err != nil {
		log.Error("set pending status", "err", err)
	}

	dir, err := os.MkdirTemp("", "mirum-*")
	if err != nil {
		log.Error("build failed", "err", err)
		_ = setStatus(owner, repo, sha, "failure", "Build failed")
		return
	}
	defer os.RemoveAll(dir)

	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	cloneURL := authURL(push.Repo.CloneURL)

	if out, err := runCmd(dir, "git", "clone", "--depth=1", "--branch", branch, cloneURL, "."); err != nil {
		log.Error("build failed", "err", err, "output", out)
		_ = setStatus(owner, repo, sha, "failure", "Build failed")
		return
	}

	if err := runStarlark(dir); err != nil {
		log.Error("build failed", "err", err)
		_ = setStatus(owner, repo, sha, "failure", "Build failed")
		return
	}

	log.Info("build passed")
	_ = setStatus(owner, repo, sha, "success", "Build passed")
}

func setStatus(owner, repo, sha, state, description string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/statuses/%s", owner, repo, sha)

	body, _ := json.Marshal(map[string]string{
		"state":       state,
		"description": description,
		"context":     "mirum",
	})

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %d: %s", resp.StatusCode, b)
	}
	return nil
}

func verifySignature(payload []byte, signature string) bool {
	sig, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	decoded, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(*secret))
	mac.Write(payload)
	return hmac.Equal(mac.Sum(nil), decoded)
}

func authURL(cloneURL string) string {
	if *token == "" {
		return cloneURL
	}
	u, err := url.Parse(cloneURL)
	if err != nil {
		return cloneURL
	}
	u.User = url.UserPassword("x-access-token", *token)
	return u.String()
}

func splitFullName(fullName string) (string, string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return fullName, ""
	}
	return parts[0], parts[1]
}

type taskCtx struct {
	dir string
}

var _ starlark.HasAttrs = (*taskCtx)(nil)

func (c *taskCtx) String() string        { return "ctx" }
func (c *taskCtx) Type() string           { return "ctx" }
func (c *taskCtx) Freeze()                {}
func (c *taskCtx) Truth() starlark.Bool   { return true }
func (c *taskCtx) Hash() (uint32, error)  { return 0, fmt.Errorf("unhashable: ctx") }
func (c *taskCtx) AttrNames() []string    { return []string{"shell"} }

func (c *taskCtx) Attr(name string) (starlark.Value, error) {
	if name == "shell" {
		return starlark.NewBuiltin("ctx.shell", c.shell), nil
	}
	return nil, nil
}

func (c *taskCtx) shell(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var cmd string
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &cmd); err != nil {
		return nil, err
	}
	proc := exec.Command("bash", "-c", cmd)
	proc.Dir = c.dir
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	err := proc.Run()
	if err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func runStarlark(dir string) error {
	thread := &starlark.Thread{Name: "mirum"}
	globals, err := starlark.ExecFile(thread, filepath.Join(dir, *script), nil, nil)
	if err != nil {
		return err
	}

	projectFn, ok := globals["project"]
	if !ok {
		return fmt.Errorf("%s: project() not defined", *script)
	}
	fn, ok := projectFn.(starlark.Callable)
	if !ok {
		return fmt.Errorf("%s: project is not a function", *script)
	}

	ctx := &taskCtx{dir: dir}
	_, err = starlark.Call(thread, fn, starlark.Tuple{ctx}, nil)
	return err
}

func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func main() {
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "error: --token is required")
		flag.Usage()
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		if len(*secret) > 0 && !verifySignature(body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		event := r.Header.Get("X-GitHub-Event")
		if event == "ping" {
			fmt.Fprintln(w, "pong")
			return
		}

		if event != "push" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var push pushEvent
		if err := json.Unmarshal(body, &push); err != nil {
			http.Error(w, "parse payload", http.StatusBadRequest)
			return
		}

		if push.After == "" || push.After == "0000000000000000000000000000000000000000" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if !strings.HasPrefix(push.Ref, "refs/heads/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		slog.Info("push", "repo", push.Repo.FullName, "ref", push.Ref, "sha", push.After[:8])
		w.WriteHeader(http.StatusAccepted)

		go processPush(push)
	})

	slog.Info("listening", "addr", *addr)

	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
