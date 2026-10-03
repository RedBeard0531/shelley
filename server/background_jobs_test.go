package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// backgroundJobNotices returns the conversation's background job notices.
func backgroundJobNotices(t *testing.T, database *db.DB, conversationID string) []backgroundJobUserData {
	t.Helper()
	msgs, err := database.ListMessages(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	var out []backgroundJobUserData
	for _, m := range msgs {
		if m.Type != string(db.MessageTypeUser) || m.UserData == nil {
			continue
		}
		var data backgroundJobUserData
		if err := json.Unmarshal([]byte(*m.UserData), &data); err != nil {
			t.Fatal(err)
		}
		if data.BackgroundJobID != "" {
			out = append(out, data)
		}
	}
	return out
}

// A command that outlives the foreground threshold returns a backgrounded
// result, and its exit later wakes the idle conversation with exactly one
// attributed notice.
func TestBashBackgroundJobNotifiesConversation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	server, database, llmSvc := newTestServer(t)
	defer stopActiveConversationLoops(server)
	server.toolSetConfig.BashBackgroundAfter = 50 * time.Millisecond

	conv, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	gate := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}

	postChatMessage(t, server, id, "bash: echo started; read -r _ < "+gate+"; echo finished")
	waitForMessageContaining(t, database, id, "moved to background job", 10*time.Second)
	waitForIdle(t, server, id)
	if n := len(backgroundJobNotices(t, database, id)); n != 0 {
		t.Fatalf("%d notices before the job exited", n)
	}

	if err := os.WriteFile(gate, []byte("go\n"), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return len(backgroundJobNotices(t, database, id)) == 1 })
	notice := backgroundJobNotices(t, database, id)[0]
	if !strings.Contains(notice.Text, "finished: exit 0") || !strings.Contains(notice.Text, "started\nfinished") {
		t.Errorf("notice = %q", notice.Text)
	}
	// The notice starts a turn in which the model sees it attributed.
	waitFor(t, 10*time.Second, func() bool {
		for _, req := range llmSvc.GetRecentRequests() {
			for _, msg := range req.Messages {
				if msg.Role == llm.MessageRoleUser && strings.Contains(messageText(msg), `<background_job id="`+notice.BackgroundJobID+`">`) {
					return true
				}
			}
		}
		return false
	})
	waitForIdle(t, server, id)
	if n := len(backgroundJobNotices(t, database, id)); n != 1 {
		t.Fatalf("%d notices, want 1", n)
	}
}
