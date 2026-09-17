package client

import "testing"

func TestBuildChatRequestBodyIncludesSenderForEitherConversationDirection(t *testing.T) {
	t.Setenv("SHELLEY_CONVERSATION_ID", "sender-conversation")

	for _, target := range []string{"parent-conversation", "child-conversation"} {
		t.Run(target, func(t *testing.T) {
			got := buildChatRequestBody("progress", "predictable", "/tmp/work", false, target)
			if got["sender_conversation_id"] != "sender-conversation" {
				t.Fatalf("sender_conversation_id = %#v, want sender-conversation", got["sender_conversation_id"])
			}
		})
	}
}

func TestBuildChatRequestBodyOmitsSenderForSelfChat(t *testing.T) {
	t.Setenv("SHELLEY_CONVERSATION_ID", "same-conversation")

	got := buildChatRequestBody("resume", "", "", false, "same-conversation")
	if _, ok := got["sender_conversation_id"]; ok {
		t.Fatalf("self-chat request unexpectedly included sender_conversation_id: %#v", got)
	}
}

func TestBuildChatRequestBodyOmitsSenderOutsideShelleyTool(t *testing.T) {
	t.Setenv("SHELLEY_CONVERSATION_ID", "")

	got := buildChatRequestBody("hello", "", "", false, "parent-conversation")
	if _, ok := got["sender_conversation_id"]; ok {
		t.Fatalf("ordinary client request unexpectedly included sender_conversation_id: %#v", got)
	}
}
