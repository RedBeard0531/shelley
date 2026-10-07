package claudetool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsEmoji(t *testing.T) {
	for _, s := range []string{"👍", "❤️", "1️⃣", "🇫🇷", "👩🏽‍💻", "✅", "🎉", "‼️", "⁉️", "〽️"} {
		if !isEmoji(s) {
			t.Errorf("isEmoji(%q) = false", s)
		}
	}
	for _, s := range []string{"", "ok", "1", "👍👍", "…", " 👍x", ":+1:", "->", "\uFE0F", "a\uFE0F", ".\uFE0F", "!"} {
		if isEmoji(s) {
			t.Errorf("isEmoji(%q) = true", s)
		}
	}
}

type noMessages struct{}

func (noMessages) FindUserMessage(context.Context, string) (UserMessage, bool, error) {
	return UserMessage{}, false, nil
}

func TestMessageUserBlankPrefixRefused(t *testing.T) {
	for _, in := range []MessageUserInput{
		{Reaction: "👍", MessagePrefix: " \n "},
		{Text: "hi", MessagePrefix: "\t", EndTurn: true},
	} {
		if out := runMessageUser(context.Background(), noMessages{}, nil, t.TempDir(), in); out.Error == nil {
			t.Errorf("%+v: sent", in)
		}
	}
}

// fakeChat records what it is sent, refusing messages with sendErr and
// reactions with reactErr.
type fakeChat struct {
	sent              []string
	sendErr, reactErr error
}

func (c *fakeChat) Send(_ context.Context, text string) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	c.sent = append(c.sent, "send "+text)
	return nil
}

func (c *fakeChat) React(_ context.Context, target UserMessage, emoji string) error {
	if c.reactErr != nil {
		return c.reactErr
	}
	c.sent = append(c.sent, "react "+emoji+" "+target.ExternalID)
	return nil
}

type oneMessage struct{ UserMessage }

func (f oneMessage) FindUserMessage(_ context.Context, prefix string) (UserMessage, bool, error) {
	return f.UserMessage, strings.HasPrefix(f.Text, prefix), nil
}

// The chat gets the message, then the reaction. A refused message is an
// error, so the agent learns it was not delivered; a reaction refused after
// its message went out leaves the call standing, with a note.
func TestMessageUserSendsToChat(t *testing.T) {
	finder := oneMessage{UserMessage{ID: "m1", Text: "hello there", ExternalID: "ext-1"}}
	reply := MessageUserInput{Text: "hi", MessagePrefix: "hello", Reaction: "👍"}
	chat := &fakeChat{}
	out := runMessageUser(t.Context(), finder, chat, t.TempDir(), reply)
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	if want := []string{"send hi", "react 👍 ext-1"}; !slices.Equal(chat.sent, want) {
		t.Fatalf("sent %q, want %q", chat.sent, want)
	}

	chat = &fakeChat{sendErr: errors.New("opted_out")}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), reply)
	if out.Error == nil || !strings.Contains(out.Error.Error(), "opted_out") || len(chat.sent) != 0 {
		t.Fatalf("refused send: %v, sent %q", out.Error, chat.sent)
	}
	if d, _ := out.Display.(MessageUserDisplay); !d.ChatFailed {
		t.Fatalf("refused send is not marked for brief view: %+v", out.Display)
	}

	chat = &fakeChat{reactErr: errors.New("line_paused")}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), reply)
	if out.Error != nil || out.Display == nil || !strings.Contains(out.LLMContent[0].Text, "line_paused") || strings.Contains(out.LLMContent[0].Text, "reacted") {
		t.Fatalf("refused reaction after message: %+v", out)
	}
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), MessageUserInput{MessagePrefix: "hello", Reaction: "👍"})
	if out.Error == nil || !strings.Contains(out.Error.Error(), "line_paused") {
		t.Fatalf("refused reaction: %v", out.Error)
	}

	// Mistakes in the call reach no chat, and are not the chat's failures.
	chat = &fakeChat{}
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range []MessageUserInput{
		{Text: "hi", MessagePrefix: "nope"},
		{Text: "see attached", Attachments: []string{file}},
	} {
		out = runMessageUser(t.Context(), finder, chat, t.TempDir(), in)
		if out.Error == nil || out.Display != nil || len(chat.sent) != 0 {
			t.Fatalf("%+v: %+v, sent %q", in, out, chat.sent)
		}
	}

	// ‼️ is a tapback, though ‼ is punctuation.
	out = runMessageUser(t.Context(), finder, chat, t.TempDir(), MessageUserInput{MessagePrefix: "hello", Reaction: "‼️"})
	if out.Error != nil || !slices.Equal(chat.sent, []string{"react ‼️ ext-1"}) {
		t.Fatalf("‼️: %v, sent %q", out.Error, chat.sent)
	}
	tool := MessageUserTool(finder, chat, NewMutableWorkingDir(t.TempDir()))
	if !tool.Sequential {
		t.Fatal("message_user is not sequential, so the chat could get messages out of order")
	}
	if !strings.Contains(tool.Description, "phone") || strings.Contains(tool.Description, "Attach files") {
		t.Fatalf("description for a chat: %s", tool.Description)
	}
	if MessageUserTool(finder, nil, NewMutableWorkingDir(t.TempDir())).Description != messageUserDescription {
		t.Fatal("description without a chat changed")
	}
}
