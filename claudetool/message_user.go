package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"shelley.exe.dev/llm"
)

// MessageUserName is the tool through which the agent talks to the user in a
// chat-style conversation: the UI shows the user only their own messages and
// what the agent sends with this tool.
const MessageUserName = "message_user"

// UserMessage is a message the user typed.
type UserMessage struct {
	ID string
	// SequenceID is the message's sequence_id, or for a compaction's copy
	// its original's.
	SequenceID int64
	Text       string
	// ExternalID is the external chat's id for the message, if it came
	// from one (see UserChat).
	ExternalID string
}

// UserMessageFinder resolves the message_prefix of a message_user call. It is
// implemented by the server package to avoid import cycles.
type UserMessageFinder interface {
	// FindUserMessage returns the newest message the user typed whose text,
	// with runs of whitespace collapsed, starts with prefix, also so
	// collapsed. ok is false when no message matches.
	FindUserMessage(ctx context.Context, prefix string) (m UserMessage, ok bool, err error)
}

// UserChat is an external chat the conversation is bound to, such as the
// user's phone. What message_user sends goes there too. It is implemented
// by the server package. Errors say whether the chat refused or delivery
// is unknown.
type UserChat interface {
	// Send delivers a text message. Chats take no attachments yet.
	Send(ctx context.Context, text string) error
	// React delivers a reaction to target, the user's message.
	React(ctx context.Context, target UserMessage, emoji string) error
}

// MessageUserInput is the message_user tool's input.
type MessageUserInput struct {
	Text          string   `json:"text"`
	MessagePrefix string   `json:"message_prefix"`
	Reaction      string   `json:"reaction"`
	Attachments   []string `json:"attachments"`
	EndTurn       bool     `json:"end_turn"`
}

// MessageUserDisplay is the Display of a successful message_user call. It is
// persisted with the tool result, which is how the UI learns which message a
// reply or reaction targets and which files it may serve.
type MessageUserDisplay struct {
	// TargetMessageID is the resolved message_prefix: the message_id of the
	// user's message.
	TargetMessageID string `json:"target_message_id,omitempty"`
	// TargetSequenceID is the target's sequence_id, or for a compaction's copy
	// its original's. Forks copy messages with new ids but their sequence_ids,
	// and copies record their original's, so it finds the target's copies.
	TargetSequenceID int64 `json:"target_sequence_id,omitempty"`
	// TargetExcerpt is the start of the target's text, for quoting it.
	TargetExcerpt string                  `json:"target_excerpt,omitempty"`
	Attachments   []MessageUserAttachment `json:"attachments,omitempty"`
	// ChatFailed marks a failed call that failed in the user's chat, not
	// for a mistake in the call: brief view shows it, since the agent may
	// have nothing else to show.
	ChatFailed bool `json:"chat_failed,omitempty"`
}

// MessageUserAttachment is a file sent with a message. Files are referenced
// where they are, not copied.
type MessageUserAttachment struct {
	Path string `json:"path"` // absolute
	Name string `json:"name"`
	Size int64  `json:"size"`
}

const messageUserDescription = `Send a message to the user. The user sees their own messages and what you send with this tool; your other output is hidden from them.
Write plain text, not markdown.
To reply or react to one of the user's messages, set message_prefix to the beginning of that message, copied verbatim.
To react, set reaction to a single emoji; text is then optional.
Attach files by path.
Set end_turn when you are done and waiting for the user; then call this tool alone.`

// messageUserChatDescription replaces messageUserDescription's attachment
// line when the user reads in a UserChat.
const messageUserChatDescription = `The user is texting you from their phone (iMessage, RCS, or SMS) and sees nothing you write outside this tool: always answer with it. Keep messages short and send few: after 5 messages without a reply, the chat takes no more until the user writes. Attachments are not supported.`

const messageUserSchema = `{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "Plain-text message (no markdown)."},
    "message_prefix": {"type": "string", "description": "Beginning of the user's message to reply or react to, copied verbatim."},
    "reaction": {"type": "string", "description": "A single emoji to react to the message_prefix message with."},
    "attachments": {"type": "array", "items": {"type": "string"}, "description": "Paths of files to send."},
    "end_turn": {"type": "boolean", "description": "End your turn after sending."}
  }
}`

// targetExcerptRunes is how much of the target's text the display quotes.
const targetExcerptRunes = 200

// MessageUserTool returns the message_user tool. chat, if not nil, is the
// external chat its messages also go to.
func MessageUserTool(finder UserMessageFinder, chat UserChat, wd *MutableWorkingDir) *llm.Tool {
	description := messageUserDescription
	if chat != nil {
		description = strings.Replace(description, "Attach files by path.", messageUserChatDescription, 1)
	}
	return &llm.Tool{
		Name:        MessageUserName,
		Description: description,
		InputSchema: llm.MustSchema(messageUserSchema),
		// Messages reach the user in the order the agent wrote them.
		Sequential: true,
		EndsTurnWhen: func(input json.RawMessage) bool {
			var in MessageUserInput
			return json.Unmarshal(input, &in) == nil && in.EndTurn
		},
		Run: llm.RunJSON(func(ctx context.Context, in MessageUserInput) llm.ToolOut {
			return runMessageUser(ctx, finder, chat, wd.Get(), in)
		}),
	}
}

func runMessageUser(ctx context.Context, finder UserMessageFinder, chat UserChat, cwd string, in MessageUserInput) llm.ToolOut {
	in.Text = strings.TrimSpace(in.Text)
	in.Reaction = strings.TrimSpace(in.Reaction)
	prefixSet := in.MessagePrefix != ""
	in.MessagePrefix = NormalizeSpace(in.MessagePrefix)
	if in.Text == "" && in.Reaction == "" && len(in.Attachments) == 0 {
		return llm.ErrorfToolOut("nothing to send: set text, reaction, or attachments")
	}
	if prefixSet && in.MessagePrefix == "" {
		return llm.ErrorfToolOut("message_prefix is blank; nothing was sent")
	}
	if in.Reaction != "" {
		if in.MessagePrefix == "" {
			return llm.ErrorfToolOut("reaction requires message_prefix, the message to react to")
		}
		if !isEmoji(in.Reaction) {
			return llm.ErrorfToolOut("reaction must be a single emoji, got %q", in.Reaction)
		}
	}
	if chat != nil && len(in.Attachments) > 0 {
		return llm.ErrorfToolOut("the user's chat takes no attachments; nothing was sent")
	}

	var display MessageUserDisplay
	for _, p := range in.Attachments {
		a, err := messageUserAttachment(cwd, p)
		if err != nil {
			return llm.ErrorToolOut(err)
		}
		if !slices.ContainsFunc(display.Attachments, func(b MessageUserAttachment) bool { return b.Path == a.Path }) {
			display.Attachments = append(display.Attachments, a)
		}
	}

	var result, quoted string
	var target UserMessage
	if in.MessagePrefix != "" {
		found, ok, err := finder.FindUserMessage(ctx, in.MessagePrefix)
		if err != nil {
			return llm.ErrorfToolOut("find user message: %w", err)
		}
		if !ok {
			return llm.ErrorfToolOut("no user message starts with %q; nothing was sent. Copy the beginning of the user's message verbatim", in.MessagePrefix)
		}
		target = found
		display.TargetMessageID = target.ID
		display.TargetSequenceID = target.SequenceID
		display.TargetExcerpt = excerpt(target.Text, targetExcerptRunes)
		quoted = fmt.Sprintf("%q", excerpt(target.Text, 80))
		switch {
		case in.Text == "" && len(in.Attachments) == 0:
			result = "Reacted " + in.Reaction + " to the user's message " + quoted + "."
		case in.Reaction != "":
			result = "Sent, in response to the user's message " + quoted + ", and reacted " + in.Reaction + " to it."
		default:
			result = "Sent, in response to the user's message " + quoted + "."
		}
	} else {
		result = "Sent."
	}
	if chat != nil {
		// The message first: it matters more, and a chat that refuses
		// reactions refuses messages too, so a reaction rarely fails after
		// its message went out. When one does, the call stands.
		failed := func(format string, err error) llm.ToolOut {
			return llm.ToolOut{Error: fmt.Errorf(format, err), Display: MessageUserDisplay{ChatFailed: true}}
		}
		if in.Text != "" {
			if err := chat.Send(ctx, in.Text); err != nil {
				return failed("sending to the user's chat failed: %w", err)
			}
		}
		if in.Reaction != "" {
			err := chat.React(ctx, target, in.Reaction)
			if err != nil && in.Text == "" {
				return failed("reacting in the user's chat failed: %w", err)
			}
			if err != nil {
				result = "Sent, in response to the user's message " + quoted + ". But the reaction " + in.Reaction + " failed in the user's chat: " + err.Error()
			}
		}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(result), Display: display, EndTurn: in.EndTurn}
}

func messageUserAttachment(cwd, p string) (MessageUserAttachment, error) {
	if strings.TrimSpace(p) == "" {
		return MessageUserAttachment{}, fmt.Errorf("empty attachment path; nothing was sent")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil {
		return MessageUserAttachment{}, fmt.Errorf("attachment %s: %w; nothing was sent", p, err)
	}
	if !fi.Mode().IsRegular() {
		return MessageUserAttachment{}, fmt.Errorf("attachment %s is not a regular file; nothing was sent", p)
	}
	return MessageUserAttachment{Path: p, Name: fi.Name(), Size: fi.Size()}, nil
}

// isEmoji reports whether s plausibly is one emoji: a single grapheme
// cluster built on a symbol (👍, 🇫🇷, 👩🏽‍💻) or a keycap (1️⃣).
// punctuationEmoji are the emoji that Unicode classes as punctuation.
const punctuationEmoji = "‼⁉〰〽"

func isEmoji(s string) bool {
	if uniseg.GraphemeClusterCount(s) != 1 {
		return false
	}
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.Is(unicode.So, r) || r == '\u20E3' || strings.ContainsRune(punctuationEmoji, r)
	})
}

// NormalizeSpace collapses each run of whitespace in s to one space and trims
// the ends. message_prefix is matched on normalized text, so a model that
// re-wraps or re-indents the user's text still finds the message.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func excerpt(s string, n int) string {
	s = NormalizeSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
