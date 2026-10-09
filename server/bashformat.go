package server

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"

	"shelley.exe.dev/llm"
)

// foldMaxBytes caps how much of the folded (single-line) command form we
// transmit. It overshoots the UI's display window so the UI can detect
// truncation for its "..." marker; the slack also absorbs some UTF-8/UTF-16
// drift (the UI truncates in UTF-16 code units). Keep in sync with
// SUMMARY_MAX_LEN in ui/src/vue/components/tools/BashTool.vue.
const foldMaxBytes = 450

// bashDisplayForms returns human-readable display forms of a bash command:
// expanded (pretty-printed, multi-line), folded (single-line), and elided
// (the folded form with a leading run of uninteresting prefix statements —
// cd calls, environment assignments, exports of assignments — dropped).
// elided is empty when there is nothing worth dropping. ok is false when the
// command does not parse, in which case callers should display the raw text.
// This never runs the command.
func bashDisplayForms(cmd string) (expanded, folded, elided string, ok bool) {
	p := syntax.NewParser(syntax.KeepComments(true))
	file, err := p.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return "", "", "", false
	}
	var expBuf strings.Builder
	if err := syntax.NewPrinter(syntax.Indent(4), syntax.BinaryNextLine(true)).Print(&expBuf, file); err != nil {
		return "", "", "", false
	}
	expanded = strings.TrimSuffix(expBuf.String(), "\n")

	var foldBuf strings.Builder
	if err := syntax.NewPrinter(syntax.SingleLine(true)).Print(&foldBuf, file); err != nil {
		return "", "", "", false
	}
	folded = truncateFolded(strings.TrimSuffix(foldBuf.String(), "\n"))

	if rem, dropped := splitBoringPrefix(file.Stmts); dropped && len(rem) > 0 {
		// The remainder is a display form; drop the comments that came
		// along on the statement nodes.
		display := make([]*syntax.Stmt, len(rem))
		for i, s := range rem {
			if len(s.Comments) == 0 {
				display[i] = s
				continue
			}
			cs := *s
			cs.Comments = nil
			display[i] = &cs
		}
		var b strings.Builder
		if err := syntax.NewPrinter(syntax.SingleLine(true)).Print(&b, &syntax.File{Stmts: display}); err != nil {
			return "", "", "", false
		}
		elided = truncateFolded(strings.TrimSuffix(b.String(), "\n"))
	}
	return expanded, folded, elided, true
}

// splitBoringPrefix returns the statements remaining after dropping a
// leading run of uninteresting prefix statements, and whether anything was
// dropped. The remainder may be empty when every statement is prefix (the
// caller must then keep the full command); scanning descends into the left
// side of && chains.
func splitBoringPrefix(stmts []*syntax.Stmt) (rem []*syntax.Stmt, elided bool) {
	if len(stmts) == 0 {
		return stmts, false
	}
	first, rest := stmts[0], stmts[1:]
	if bc, ok := first.Cmd.(*syntax.BinaryCmd); ok && bc.Op == syntax.AndStmt {
		// Left-associative && chain: (X) && Y. The boring prefix may end
		// partway into X (or cover all of it), so scan X first.
		xRem, xElided := splitBoringPrefix([]*syntax.Stmt{bc.X})
		if !xElided {
			return stmts, false
		}
		if len(xRem) == 0 {
			// X was entirely prefix; the chain continues into Y.
			rem, _ = splitBoringPrefix(append([]*syntax.Stmt{bc.Y}, rest...))
			return rem, true
		}
		nbc := *bc
		nbc.X = xRem[0]
		ns := *first
		ns.Cmd = &nbc
		return append([]*syntax.Stmt{&ns}, rest...), true
	}
	if whole, cleaned := boringPrefixUnit(first); whole {
		rem, _ = splitBoringPrefix(rest)
		return rem, true
	} else if cleaned != nil {
		return append([]*syntax.Stmt{cleaned}, rest...), true
	}
	return stmts, false
}

// boringPrefixUnit reports whether s is — or starts with — an uninteresting
// prefix construct. wholeBoring is true when the entire statement is one:
// a `cd <dir>` call (a bare `cd` goes to $HOME and is never dropped), a pure
// environment assignment, or an export whose arguments are all assignments
// (`export A=1 B cmd` exports names and runs nothing, so any bare name or
// option keeps the statement visible). Otherwise cleaned is a copy of the
// statement with leading assignments removed, for calls that both assign and
// run something (`FOO=1 ls`). Statements with redirections or negation are
// never touched.
func boringPrefixUnit(s *syntax.Stmt) (wholeBoring bool, cleaned *syntax.Stmt) {
	if s.Negated || len(s.Redirs) > 0 {
		return false, nil
	}
	switch v := s.Cmd.(type) {
	case *syntax.DeclClause:
		if v.Variant == nil || v.Variant.Value != "export" || len(v.Args) == 0 {
			return false, nil
		}
		for _, a := range v.Args {
			if a.Naked || a.Name == nil {
				return false, nil
			}
		}
		return true, nil
	case *syntax.CallExpr:
		if len(v.Args) == 0 {
			return true, nil // pure assignment: applies to the shell environment
		}
		if v.Args[0].Lit() == "cd" {
			return len(v.Args) >= 2, nil
		}
		if len(v.Assigns) > 0 {
			clean := *v
			clean.Assigns = nil
			cs := *s
			cs.Cmd = &clean
			return false, &cs
		}
	}
	return false, nil
}

// truncateFolded cuts s at foldMaxBytes without tearing a UTF-8 codepoint,
// rounding up to include the character that straddles the limit.
func truncateFolded(s string) string {
	if len(s) <= foldMaxBytes {
		return s
	}
	n := foldMaxBytes
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return s[:n]
}

// addBashDisplayForms injects formattedCommand/foldedCommand entries into a
// bash-family tool_use input JSON so the UI can render human-formatted
// commands. The raw command key is never modified. Reports whether the input
// JSON changed.
func addBashDisplayForms(content *llm.Content) bool {
	if content.Type != llm.ContentTypeToolUse || content.ToolInput == nil {
		return false
	}
	if content.ToolName != "bash" {
		return false
	}
	var input map[string]any
	if err := json.Unmarshal(content.ToolInput, &input); err != nil {
		return false
	}
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return false
	}
	expanded, folded, elided, ok := bashDisplayForms(cmd)
	if !ok {
		return false
	}
	if expanded != cmd {
		input["formattedCommand"] = expanded
	}
	// Compare within the truncated window: differences past it are invisible
	// to the UI, so they are not worth transmitting.
	if folded != truncateFolded(cmd) {
		input["foldedCommand"] = folded
	}
	if elided != "" && elided != folded {
		input["elidedCommand"] = elided
	}
	out, err := json.Marshal(input)
	if err != nil {
		return false
	}
	content.ToolInput = out
	return true
}
