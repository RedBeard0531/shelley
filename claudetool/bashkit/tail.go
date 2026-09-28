package bashkit

import (
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// TailPipe reports whether command ends in a plain line-counting `tail` that
// trims the whole command's output, as in `go test ./... 2>&1 | tail -40`. If
// so, it returns the command with that tail stage removed, plus the number of
// lines the tail would have kept.
//
// The tail must be fed by the whole command, so the pipeline it ends must be
// everything the command prints. That allows a prefix of `cd`s -- which print
// nothing -- in front of it (`cd /tmp && make 2>&1 | tail -5`), and nothing
// else. A later stage (`| tail -5 | grep x`), a redirect (`| tail -5 > out`),
// a second statement after it (`| tail -5; echo done`), any prefix that can
// print (`a && b | tail -5`, `mkdir x && b | tail -5`), or anything but a bare
// line count (`tail -c 100`, `tail -f`, `tail -n +5`, operands, an assignment
// prefix like `x=$(date) tail -5`) all leave the command alone, because
// removing the tail would change what the command does.
//
// The caller is expected to run the stripped command and truncate the output
// itself: the tail would otherwise hold the output back until the command
// finishes, which is exactly what we want to avoid.
func TailPipe(command string) (stripped string, lines int, ok bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) == 0 {
		return "", 0, false
	}
	// Statements before the last one are `cd`s or the command prints more
	// than the tail's input.
	for _, stmt := range file.Stmts[:len(file.Stmts)-1] {
		if !isSilentCd(stmt) {
			return "", 0, false
		}
	}
	stmt, pipe, ok := tailedPipeline(file.Stmts[len(file.Stmts)-1])
	if !ok {
		return "", 0, false
	}
	lines, ok = tailLineCount(pipe.Y)
	if !ok {
		return "", 0, false
	}
	// Drop the tail stage, then print what is left: printing the tree rather
	// than slicing the source keeps constructs whose text is not confined to
	// the part before the tail -- heredoc bodies, line continuations --
	// intact, and keeps any `cd` prefix.
	*stmt = *pipe.X
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, file); err != nil {
		return "", 0, false
	}
	// Print ends a file with a newline; neither we nor bash need it.
	return strings.TrimSuffix(b.String(), "\n"), lines, true
}

// tailedPipeline returns the statement holding a pipeline whose last stage is
// a call to tail, following a chain of `&&`/`||` whose earlier stages are
// `cd`s. It reports false for anything else: a negated, backgrounded or
// redirected statement, a chain with a stage that can print, or a pipeline
// the tail does not end.
func tailedPipeline(stmt *syntax.Stmt) (tailed *syntax.Stmt, pipe *syntax.BinaryCmd, ok bool) {
	if stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 {
		return nil, nil, false
	}
	if bin, isChain := stmt.Cmd.(*syntax.BinaryCmd); isChain {
		switch bin.Op {
		case syntax.AndStmt, syntax.OrStmt:
			if !isSilentCd(bin.X) {
				return nil, nil, false
			}
			return tailedPipeline(bin.Y)
		}
	}
	pipe, isPipe := stmt.Cmd.(*syntax.BinaryCmd)
	if !isPipe || (pipe.Op != syntax.Pipe && pipe.Op != syntax.PipeAll) {
		return nil, nil, false
	}
	return stmt, pipe, true
}

// isSilentCd reports whether a statement prints nothing: a `cd`, or a chain of
// them. `cd -` prints the directory it lands in, so it does not count.
func isSilentCd(stmt *syntax.Stmt) bool {
	if stmt.Negated || stmt.Background || stmt.Coprocess {
		return false
	}
	if bin, isChain := stmt.Cmd.(*syntax.BinaryCmd); isChain {
		switch bin.Op {
		case syntax.AndStmt, syntax.OrStmt:
			return isSilentCd(bin.X) && isSilentCd(bin.Y)
		}
	}
	call, isCall := stmt.Cmd.(*syntax.CallExpr)
	if !isCall || len(call.Args) == 0 || call.Args[0].Lit() != "cd" {
		return false
	}
	for _, arg := range call.Args[1:] {
		if arg.Lit() == "-" {
			return false
		}
	}
	return true
}

// tailLineCount returns the number of trailing lines a call to tail keeps, or
// false if the call is anything but a plain line-counting tail.
func tailLineCount(stmt *syntax.Stmt) (int, bool) {
	if stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 {
		return 0, false
	}
	call, isCall := stmt.Cmd.(*syntax.CallExpr)
	// Assignments are evaluated before the command runs, so dropping them
	// with the tail would drop their side effects (`x=$(date) tail -5`).
	if !isCall || len(call.Args) == 0 || len(call.Assigns) > 0 || call.Args[0].Lit() != "tail" {
		return 0, false
	}
	args := call.Args[1:]
	lines := 10 // what a bare `tail` keeps
	for i := 0; i < len(args); i++ {
		arg := args[i].Lit()
		value := ""
		switch {
		case arg == "-n" || arg == "--lines":
			if i+1 >= len(args) {
				return 0, false
			}
			i++
			value = args[i].Lit()
		case strings.HasPrefix(arg, "--lines="):
			value = strings.TrimPrefix(arg, "--lines=")
		case strings.HasPrefix(arg, "-n"):
			value = strings.TrimPrefix(arg, "-n")
		case strings.HasPrefix(arg, "-") && isDigits(arg[1:]):
			value = arg[1:]
		default:
			// An operand (a file to read) or a flag we don't understand:
			// leave the command alone.
			return 0, false
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || strings.HasPrefix(value, "+") {
			// Not a plain line count: a byte count, a variable, or `+5`,
			// which means "from line 5" and so is not a tail at all.
			return 0, false
		}
		lines = n
	}
	return lines, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
