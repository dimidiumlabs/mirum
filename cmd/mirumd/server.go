// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"dimidiumlabs/mirum/internal/config"
	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/forges"
	"dimidiumlabs/mirum/internal/protocol/pb"
)

// server holds the shared application state.
type server struct {
	cfg   *appConfig
	db    *database.DB
	forge forges.Forge

	queue       chan *pb.Task
	tasks       sync.Map // task_id → *forges.PushEvent
	taskCounter atomic.Int64
}

// Close releases resources owned by the server. Call exactly once, after
// all HTTP servers have finished Shutdown.
func (s *server) Close() {
	close(s.queue)
	s.db.Close()
}

// PurgeSessions periodically deletes expired sessions until ctx is cancelled.
func (s *server) PurgeSessions(ctx context.Context) {
	ticker := time.NewTicker(config.SessionPurgeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.db.PurgeExpiredSessions(ctx); err != nil {
				slog.Error("purge sessions", "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) enqueue(ev *forges.PushEvent) string {
	s.taskCounter.Add(1)
	id := fmt.Sprintf("task-%d", s.taskCounter.Load())

	slog.Info("push", "repo", ev.Owner+"/"+ev.Repo, "branch", ev.Branch, "sha", ev.SHA[:8], "task", id)

	s.tasks.Store(id, ev)
	_ = s.forge.SetStatus(context.Background(), ev, forges.StatusPending, "Queued")

	s.queue <- &pb.Task{
		Id:           id,
		CloneUrl:     s.forge.AuthURL(ev.CloneURL),
		Branch:       ev.Branch,
		Sha:          ev.SHA,
		RepoFullName: ev.Owner + "/" + ev.Repo,
	}

	return id
}

func (s *server) complete(ctx context.Context, taskID string, success bool, errMsg string) error {
	val, ok := s.tasks.LoadAndDelete(taskID)
	if !ok {
		return fmt.Errorf("unknown task: %s", taskID)
	}
	ev := val.(*forges.PushEvent)

	st := forges.StatusSuccess
	desc := "Build passed"
	if !success {
		st = forges.StatusFailure
		desc = "Build failed"
		if errMsg != "" {
			desc = errMsg
		}
	}

	if err := s.forge.SetStatus(ctx, ev, st, desc); err != nil {
		slog.Error("set status", "task", taskID, "err", err)
	}

	slog.Info("task complete", "id", taskID, "success", success)
	return nil
}
