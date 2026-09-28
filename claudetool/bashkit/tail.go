package bashkit

import (
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// TailPipe reports whether command is exactly one pipeline whose last stage is
// a plain `tail` trimming the pipeline's output to its last lines, as in
// `go test ./... 2>&1 | tail -40`. If so, it returns the pipeline with that
// tail stage removed, plus the number of lines the tail would have kept.
//
// Only the whole command counts. A later stage (`| tail -5 | grep x`), a
// redirect (`| tail -5 > out`), a second statement (`| tail -5; echo done`),
// a pipeline that is only part of a larger command (`a && b | tail -5`), or
// anything but a bare line count (`tail -c 100`, `tail -f`, `tail -n +5`,
// operands, an assignment prefix like `x=$(date) tail -5`) all leave the
// command alone, because removing the tail would change what the command does.
//
// The caller is expected to run the stripped command and truncate the output
// itself: the tail would otherwise hold the output back until the command
// finishes, which is exactly what we want to avoid.
func TailPipe(command string) (stripped string, lines int, ok bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return "", 0, false
	}
	stmt := file.Stmts[0]
	if stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 {
		return "", 0, false
	}
	pipe, isPipe := stmt.Cmd.(*syntax.BinaryCmd)
	if !isPipe || (pipe.Op != syntax.Pipe && pipe.Op != syntax.PipeAll) {
		return "", 0, false
	}
	lines, ok = tailLineCount(pipe.Y)
	if !ok {
		return "", 0, false
	}
	// Print the pipeline upstream of the tail rather than slicing the source
	// at the pipe: a heredoc's body or a line continuation is not confined to
	// the text before the tail, and printing the parsed form handles both.
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, pipe.X); err != nil {
		return "", 0, false
	}
	return b.String(), lines, true
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
