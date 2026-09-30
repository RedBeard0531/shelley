package committour

import "strings"

// MechanicalReason names why a patch fragment needs no narration, or returns
// "" when a reader should judge it. Only structurally provable cases qualify;
// small edits are never mechanical just because they are small.
func MechanicalReason(fragment string) string {
	if GeneratedPath(Meta(fragment).File) {
		return "generated"
	}
	if strings.Contains(fragment, "\nGIT binary patch\n") || strings.Contains(fragment, "\nBinary files ") {
		return "binary"
	}
	if !strings.Contains(fragment, "\n@@") {
		if strings.Contains(fragment, "\nrename from ") || strings.Contains(fragment, "\nold mode ") {
			return "rename-or-mode"
		}
		return ""
	}
	var adds, dels []string
	inHunk := false
	for _, line := range strings.Split(fragment, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			adds = append(adds, line[1:])
		case inHunk && strings.HasPrefix(line, "-"):
			dels = append(dels, line[1:])
		}
	}
	if len(adds) == len(dels) && len(adds) > 0 {
		same := true
		for i := range adds {
			if strings.Join(strings.Fields(adds[i]), "") != strings.Join(strings.Fields(dels[i]), "") {
				same = false
				break
			}
		}
		if same {
			return "whitespace-only"
		}
	}
	return ""
}

// GeneratedPath reports whether a path looks machine-generated or otherwise
// uninteresting to narrate: lock files and common codegen output.
func GeneratedPath(path string) bool {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	switch base {
	case "go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "Cargo.lock", "poetry.lock", "uv.lock", "composer.lock", "Gemfile.lock":
		return true
	}
	for _, suffix := range []string{".pb.go", ".pb.gw.go", "_pb2.py", ".gen.go", "_gen.go", ".sql.go", ".min.js", ".min.css"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, dir := range strings.Split(path, "/") {
		if dir == "generated" || dir == "node_modules" || dir == "vendor" {
			return true
		}
	}
	return false
}
