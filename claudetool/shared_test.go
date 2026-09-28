package claudetool

import "testing"

func TestLastLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "last two of three", in: "a\nb\nc\n", n: 2, want: "b\nc\n"},
		{name: "last one", in: "a\nb\nc\n", n: 1, want: "c\n"},
		{name: "more than there are", in: "a\nb\nc\n", n: 5, want: "a\nb\nc\n"},
		{name: "no trailing newline", in: "a\nb\nc", n: 2, want: "b\nc"},
		{name: "single line", in: "a", n: 3, want: "a"},
		{name: "empty", in: "", n: 3, want: ""},
		{name: "blank lines", in: "\n\n\n", n: 2, want: "\n\n"},
		{name: "trailing newline only", in: "a\n", n: 2, want: "a\n"},
		{name: "partial last line counts", in: "a\nb", n: 1, want: "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(lastLines([]byte(tt.in), tt.n)); got != tt.want {
				t.Errorf("lastLines(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
			if got := lastLinesString(tt.in, tt.n); got != tt.want {
				t.Errorf("lastLinesString(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}
