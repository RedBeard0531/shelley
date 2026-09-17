package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

type senderRelationship string

const (
	senderRelationshipSubagent senderRelationship = "subagent"
	senderRelationshipParent   senderRelationship = "parent"
)

type senderMessageUserData struct {
	SenderConversationID string             `json:"sender_conversation_id"`
	SenderSlug           string             `json:"sender_slug"`
	SenderRelationship   senderRelationship `json:"sender_relationship"`
	// Text duplicates the final user message because the FTS trigger searches
	// user_data instead of llm_data whenever user_data is present.
	Text string `json:"Text"`
}

func provenanceEligibleConversation(conversation generated.Conversation) bool {
	return !isBtwReader(conversation) && db.ParseConversationOptions(conversation.ConversationOptions).Kind != transcriptionKind
}

// senderUserData validates CLI-supplied sender provenance against the
// conversation graph. Only the trusted local CLI may identify either side of
// a direct parent/managed-child relationship.
func (s *Server) senderUserData(ctx context.Context, target generated.Conversation, senderConversationID string) (*senderMessageUserData, error) {
	if !isLocalCLIRequest(ctx) || senderConversationID == "" || senderConversationID == target.ConversationID || !provenanceEligibleConversation(target) {
		return nil, nil
	}
	var sender generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		sender, err = q.GetConversation(ctx, senderConversationID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load chat sender %s: %w", senderConversationID, err)
	}
	if !provenanceEligibleConversation(sender) {
		return nil, nil
	}

	var relationship senderRelationship
	switch {
	case isManagedChild(sender) && sender.ParentConversationID != nil && *sender.ParentConversationID == target.ConversationID:
		relationship = senderRelationshipSubagent
	case isManagedChild(target) && target.ParentConversationID != nil && *target.ParentConversationID == senderConversationID:
		relationship = senderRelationshipParent
	default:
		return nil, nil
	}
	return &senderMessageUserData{
		SenderConversationID: senderConversationID,
		SenderSlug:           derefString(sender.Slug),
		SenderRelationship:   relationship,
	}, nil
}

func parseSenderMessageUserData(raw []byte) (senderMessageUserData, bool, error) {
	if len(raw) == 0 {
		return senderMessageUserData{}, false, nil
	}
	var data senderMessageUserData
	if err := json.Unmarshal(raw, &data); err != nil {
		return senderMessageUserData{}, false, fmt.Errorf("decode sender provenance: %w", err)
	}
	if data.SenderConversationID == "" && data.SenderSlug == "" && data.SenderRelationship == "" {
		return senderMessageUserData{}, false, nil
	}
	if data.SenderConversationID == "" ||
		(data.SenderRelationship != senderRelationshipSubagent && data.SenderRelationship != senderRelationshipParent) {
		return senderMessageUserData{}, false, fmt.Errorf("incomplete sender provenance")
	}
	return data, true, nil
}

func xmlProvenanceOpeningTag(tag, conversationID, slug string) (string, error) {
	var out bytes.Buffer
	encoder := xml.NewEncoder(&out)
	if err := encoder.EncodeToken(xml.StartElement{
		Name: xml.Name{Local: tag},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "conversation_id"}, Value: conversationID},
			{Name: xml.Name{Local: "slug"}, Value: slug},
		},
	}); err != nil {
		return "", err
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Element text needs only markup delimiters escaped. Keep whitespace and quotes
// literal so code blocks and structured reports stay readable to the model.
var provenanceTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func xmlProvenanceText(text string) string {
	return provenanceTextEscaper.Replace(text)
}

func messageWithSenderProvenance(message llm.Message, rawUserData []byte) (llm.Message, error) {
	data, ok, err := parseSenderMessageUserData(rawUserData)
	if err != nil || !ok {
		return message, err
	}
	tag := "subagent_message"
	if data.SenderRelationship == senderRelationshipParent {
		tag = "parent_message"
	}
	opening, err := xmlProvenanceOpeningTag(tag, data.SenderConversationID, data.SenderSlug)
	if err != nil {
		return message, fmt.Errorf("encode sender provenance tag: %w", err)
	}
	closing := "</" + tag + ">"

	message.Content = append([]llm.Content(nil), message.Content...)
	firstText, lastText := -1, -1
	for i := range message.Content {
		if message.Content[i].Type != llm.ContentTypeText || message.Content[i].MediaType != "" {
			continue
		}
		message.Content[i].Text = xmlProvenanceText(message.Content[i].Text)
		if firstText < 0 {
			firstText = i
		}
		lastText = i
	}
	if firstText < 0 {
		return message, fmt.Errorf("sender provenance message has no text content")
	}
	message.Content[firstText].Text = opening + "\n" + message.Content[firstText].Text
	message.Content[lastText].Text += "\n" + closing
	return message, nil
}

func messageWithContextSenderProvenance(ctx context.Context, message llm.Message) (llm.Message, error) {
	raw, err := marshalTurnUserData(ctx)
	if err != nil {
		return message, err
	}
	return messageWithSenderProvenance(message, raw)
}
