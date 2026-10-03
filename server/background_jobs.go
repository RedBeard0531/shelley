package server

import (
	"context"
	"fmt"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
)

// backgroundJobs implements claudetool.BackgroundJobs: when a backgrounded
// bash command exits, it tells the command's conversation.
type backgroundJobs struct {
	server *Server
}

var _ claudetool.BackgroundJobs = backgroundJobs{}

// Background implements claudetool.BackgroundJobs.
func (b backgroundJobs) Background(ctx context.Context, job claudetool.BackgroundJob, exited <-chan struct{}) error {
	go b.server.notifyBackgroundJobExit(job, exited)
	return nil
}

// notifyBackgroundJobExit waits for job to exit, then queues its notice in
// the job's conversation.
func (s *Server) notifyBackgroundJobExit(job claudetool.BackgroundJob, exited <-chan struct{}) {
	<-exited
	if err := s.deliverBackgroundJobNotice(context.Background(), job); err != nil {
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
