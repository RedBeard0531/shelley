package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// backgroundJobs implements claudetool.BackgroundJobs: when a backgrounded
// bash command exits, it tells the command's conversation. Jobs are recorded
// in the database so that a job outliving this process is still reported
// after a restart (see recoverBackgroundJobs).
type backgroundJobs struct {
	server *Server
}

var _ claudetool.BackgroundJobs = backgroundJobs{}

// Background implements claudetool.BackgroundJobs.
func (b backgroundJobs) Background(ctx context.Context, job claudetool.BackgroundJob, exited <-chan struct{}) error {
	if err := b.server.recordBackgroundJob(ctx, job); err != nil {
		return err
	}
	b.server.watchBackgroundJob(job, exited)
	return nil
}

// recordBackgroundJob stores job until its completion is reported.
func (s *Server) recordBackgroundJob(ctx context.Context, job claudetool.BackgroundJob) error {
	err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.InsertBackgroundJob(ctx, generated.InsertBackgroundJobParams{
			JobID:            job.ID,
			ConversationID:   job.ConversationID,
			ToolUseID:        job.ToolUseID,
			Command:          job.Command,
			Pid:              int64(job.PID),
			ProcessStartTime: int64(job.StartTime),
			LogPath:          job.LogPath,
			ExitPath:         job.ExitPath,
			StartedAt:        job.StartedAt.UTC(),
		})
	})
	if err != nil {
		return fmt.Errorf("record background job: %w", err)
	}
	return nil
}

// recoverBackgroundJobs resumes reporting the jobs a previous Shelley
// process backgrounded but did not report: jobs that exited (or vanished)
// meanwhile are reported now, running ones when they exit.
func (s *Server) recoverBackgroundJobs(ctx context.Context) error {
	var rows []generated.BackgroundJob
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		rows, err = q.ListUnnotifiedBackgroundJobs(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("list background jobs: %w", err)
	}
	for _, r := range rows {
		job := claudetool.BackgroundJob{
			ID:             r.JobID,
			ConversationID: r.ConversationID,
			ToolUseID:      r.ToolUseID,
			Command:        r.Command,
			PID:            int(r.Pid),
			StartTime:      uint64(r.ProcessStartTime),
			LogPath:        r.LogPath,
			ExitPath:       r.ExitPath,
			StartedAt:      r.StartedAt,
		}
		exited, err := job.Exited()
		if err != nil {
			s.logger.Error("Failed to watch background job", "job", job.ID, "conversation", job.ConversationID, "error", err)
			continue
		}
		s.watchBackgroundJob(job, exited)
	}
	return nil
}

// watchBackgroundJob counts job as running in its conversation until it
// exits, then queues its notice in the conversation and records that it
// did. A failed delivery is retried at the next startup.
func (s *Server) watchBackgroundJob(job claudetool.BackgroundJob, exited <-chan struct{}) {
	select {
	case <-exited:
	default:
		s.setBackgroundJobRunning(job, true)
	}
	go func() {
		<-exited
		s.setBackgroundJobRunning(job, false)
		s.reportBackgroundJobExit(job)
	}()
}

// setBackgroundJobRunning adds job to or removes it from the running set
// and refreshes the conversation list, which carries per-conversation
// counts.
func (s *Server) setBackgroundJobRunning(job claudetool.BackgroundJob, running bool) {
	s.backgroundJobsMu.Lock()
	jobs := s.runningBackgroundJobs[job.ConversationID]
	switch {
	case running && jobs == nil:
		jobs = map[string]claudetool.BackgroundJob{}
		s.runningBackgroundJobs[job.ConversationID] = jobs
		fallthrough
	case running:
		jobs[job.ID] = job
	default:
		delete(jobs, job.ID)
		if len(jobs) == 0 {
			delete(s.runningBackgroundJobs, job.ConversationID)
		}
	}
	s.backgroundJobsMu.Unlock()
	s.notifyConversationListChanged()
}

// runningBackgroundJobCounts returns the number of running background jobs
// per conversation.
func (s *Server) runningBackgroundJobCounts() map[string]int64 {
	s.backgroundJobsMu.Lock()
	defer s.backgroundJobsMu.Unlock()
	counts := make(map[string]int64, len(s.runningBackgroundJobs))
	for id, jobs := range s.runningBackgroundJobs {
		counts[id] = int64(len(jobs))
	}
	return counts
}

// runningBackgroundJobsOf returns conversationID's running jobs, oldest first.
func (s *Server) runningBackgroundJobsOf(conversationID string) []claudetool.BackgroundJob {
	s.backgroundJobsMu.Lock()
	defer s.backgroundJobsMu.Unlock()
	jobs := slices.Collect(maps.Values(s.runningBackgroundJobs[conversationID]))
	slices.SortFunc(jobs, func(a, b claudetool.BackgroundJob) int { return a.StartedAt.Compare(b.StartedAt) })
	return jobs
}

func (s *Server) reportBackgroundJobExit(job claudetool.BackgroundJob) {
	ctx := context.Background()
	err := s.deliverBackgroundJobNotice(ctx, job)
	if err == nil {
		err = s.db.QueriesTx(ctx, func(q *generated.Queries) error {
			return q.MarkBackgroundJobNotified(ctx, job.ID)
		})
	}
	if err != nil {
		s.logger.Error("Failed to deliver background job notice", "job", job.ID, "conversation", job.ConversationID, "error", err)
	}
}

// deliverBackgroundJobNotice queues job's notice as a user row whose
// user_data names the job, so the model sees it wrapped in
// <background_job> and the UI attributes it. A busy conversation takes it
// at its next LLM request; an idle one starts a turn.
func (s *Server) deliverBackgroundJobNotice(ctx context.Context, job claudetool.BackgroundJob) error {
	cm, err := s.getOrCreateConversationManager(ctx, job.ConversationID, "")
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	cm.mu.Lock()
	modelID := cm.modelID
	cm.mu.Unlock()
	outcome := job.Outcome()
	text := outcome.Notice()
	data := backgroundJobUserData{
		BackgroundJobID: job.ID,
		Command:         job.Command,
		ExitCode:        outcome.ExitCode,
		LogPath:         job.LogPath,
		Tail:            outcome.Tail,
		Text:            text,
	}
	if outcome.ExitCode != nil {
		data.Duration = outcome.Elapsed.String()
	}
	ctx = contextWithTurnUserData(ctx, data)
	return cm.InjectMessage(ctx, s, modelID, llm.UserStringMessage(text))
}

// BackgroundJobInfo describes a running background job to the UI.
type BackgroundJobInfo struct {
	JobID     string    `json:"job_id"`
	Command   string    `json:"command"`
	PGID      int       `json:"pgid"`
	LogPath   string    `json:"log_path"`
	StartedAt time.Time `json:"started_at"`
}

// handleListBackgroundJobs handles GET /api/conversation/{id}/background-jobs.
func (s *Server) handleListBackgroundJobs(w http.ResponseWriter, r *http.Request, conversationID string) {
	out := []BackgroundJobInfo{}
	for _, j := range s.runningBackgroundJobsOf(conversationID) {
		out = append(out, BackgroundJobInfo{JobID: j.ID, Command: j.Command, PGID: j.PID, LogPath: j.LogPath, StartedAt: j.StartedAt})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleKillBackgroundJob handles
// POST /api/conversation/{id}/background-jobs/{jobID}/kill. It sends
// SIGTERM to the job's process group; the job's exit then reports through
// the usual notice.
func (s *Server) handleKillBackgroundJob(w http.ResponseWriter, r *http.Request, conversationID, jobID string) {
	s.backgroundJobsMu.Lock()
	job, ok := s.runningBackgroundJobs[conversationID][jobID]
	s.backgroundJobsMu.Unlock()
	if !ok {
		http.Error(w, fmt.Sprintf("no running background job %s in this conversation", jobID), http.StatusNotFound)
		return
	}
	err := job.Kill()
	if errors.Is(err, claudetool.ErrBackgroundJobGone) {
		http.Error(w, fmt.Sprintf("background job %s is no longer running", jobID), http.StatusConflict)
		return
	}
	if err != nil {
		s.logger.Error("Failed to kill background job", "job", jobID, "conversation", conversationID, "error", err)
		http.Error(w, fmt.Sprintf("kill background job %s: %v", jobID, err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
