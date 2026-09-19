package client

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

var reasoningLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func validateReasoningLevel(level string) error {
	if level == "" {
		return nil
	}
	for _, known := range reasoningLevels {
		if level == known {
			return nil
		}
	}
	return fmt.Errorf("invalid reasoning level %q (choose one of: %s)", level, strings.Join(reasoningLevels, ", "))
}

type toolOverridesFlag map[string]string

func (f *toolOverridesFlag) String() string {
	if f == nil || len(*f) == 0 {
		return ""
	}
	values := make([]string, 0, len(*f))
	for name, state := range *f {
		values = append(values, name+"="+state)
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func (f *toolOverridesFlag) Set(value string) error {
	name, state, ok := strings.Cut(value, "=")
	name = strings.TrimSpace(name)
	state = strings.TrimSpace(state)
	if !ok || name == "" || (state != "on" && state != "off") {
		return fmt.Errorf("expected NAME=on or NAME=off")
	}
	if *f == nil {
		*f = make(toolOverridesFlag)
	}
	(*f)[name] = state
	return nil
}

type conversationOptionsWire struct {
	ToolOverrides        map[string]string `json:"tool_overrides,omitempty"`
	DisableAllTools      bool              `json:"disable_all_tools,omitempty"`
	ThinkingLevel        string            `json:"thinking_level,omitempty"`
	DisableNotifications bool              `json:"disable_notifications,omitempty"`
}

func buildConversationOptions(reasoning string, tools map[string]string, noTools, noNotify bool) (*conversationOptionsWire, error) {
	if err := validateReasoningLevel(reasoning); err != nil {
		return nil, err
	}
	for name, state := range tools {
		if strings.TrimSpace(name) == "" || (state != "on" && state != "off") {
			return nil, fmt.Errorf("invalid tool override %q=%q (expected NAME=on or NAME=off)", name, state)
		}
	}
	if reasoning == "" && len(tools) == 0 && !noTools && !noNotify {
		return nil, nil
	}
	return &conversationOptionsWire{
		ToolOverrides:        tools,
		DisableAllTools:      noTools,
		ThinkingLevel:        reasoning,
		DisableNotifications: noNotify,
	}, nil
}

func validateChatTarget(conversationID string, options *conversationOptionsWire) error {
	if conversationID != "" && options != nil {
		return fmt.Errorf("-reasoning, -tool, -no-tools, and -disable-notifications only apply to new conversations (omit -c)")
	}
	return nil
}

func httpResponseError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("HTTP %d: reading error response: %w", resp.StatusCode, err)
	}
	if detail := strings.TrimSpace(string(body)); detail != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, detail)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
