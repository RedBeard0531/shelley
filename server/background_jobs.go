package server

import (
	"context"
	"fmt"

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
	go b.server.notifyBackgroundJobExit(job, exited)
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
		go s.notifyBackgroundJobExit(job, exited)
	}
	return nil
}

// notifyBackgroundJobExit waits for job to exit, then queues its notice in
// the job's conversation and records that it did. A failed delivery is
// retried at the next startup.
func (s *Server) notifyBackgroundJobExit(job claudetool.BackgroundJob, exited <-chan struct{}) {
	<-exited
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
	text := job.Notice()
	ctx = contextWithTurnUserData(ctx, backgroundJobUserData{BackgroundJobID: job.ID, Text: text})
	return cm.InjectMessage(ctx, s, modelID, llm.UserStringMessage(text))
}
