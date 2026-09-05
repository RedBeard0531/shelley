package stt

import (
	"os"
	"strings"
)

// cpuVariantISA maps ggml's micro-arch CPU variants to the /proc/cpuinfo
// flags they require. ggml's backend scan picks the variant with the best
// self-reported score without checking the CPU actually supports its
// instructions, so a zen4 lib on an AVX-512-VNNI-masked VM is one illegal
// instruction away from killing the process. We only let variants whose
// requirements this CPU satisfies take part in the scan.
var cpuVariantISA = map[string][]string{
	"x64":            nil, // common denominator
	"sse42":          nil,
	"sandybridge":    {"avx"},
	"ivybridge":      {"avx"},
	"piledriver":     {"avx"},
	"haswell":        {"avx2", "fma"},
	"alderlake":      {"avx2", "fma", "avx512_vnni"},
	"skylakex":       {"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"},
	"cascadelake":    {"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl", "avx512_vnni"},
	"cooperlake":     {"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl", "avx512_vnni"},
	"icelake":        {"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl", "avx512_vnni"},
	"sapphirerapids": {"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl", "avx512_vnni", "amx"},
	"zen4":           {"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl", "avx512_vnni"},
}

// cpuFlags returns the flag set of the first /proc/cpuinfo processor block.
func cpuFlags() map[string]bool {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "flags") || !strings.Contains(line, ":") {
			continue
		}
		flags := make(map[string]bool)
		for _, f := range strings.Fields(strings.SplitN(line, ":", 2)[1]) {
			flags[f] = true
		}
		_ = i
		return flags
	}
	return nil
}

// permittedVariants returns the subset of cfg names whose ISA requirements
// the running CPU satisfies, always including the baseline variants.
func permittedVariants(flags map[string]bool) []string {
	var out []string
	for name, required := range cpuVariantISA {
		if len(required) == 0 {
			out = append(out, name)
			continue
		}
		ok := true
		for _, f := range required {
			if !flags[f] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, name)
		}
	}
	return out
}
