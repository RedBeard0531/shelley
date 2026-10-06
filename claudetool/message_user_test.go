package claudetool

import (
	"context"
	"testing"
)

func TestIsEmoji(t *testing.T) {
	for _, s := range []string{"👍", "❤️", "1️⃣", "🇫🇷", "👩🏽‍💻", "✅", "🎉"} {
		if !isEmoji(s) {
			t.Errorf("isEmoji(%q) = false", s)
		}
	}
	for _, s := range []string{"", "ok", "1", "👍👍", "…", " 👍x", ":+1:", "->"} {
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
		if out := runMessageUser(context.Background(), noMessages{}, t.TempDir(), in); out.Error == nil {
			t.Errorf("%+v: sent", in)
		}
	}
}
