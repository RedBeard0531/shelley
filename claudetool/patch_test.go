package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/llm"
)

func TestPatchToolAliasesSharePathLock(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "file.txt")
	if err := os.WriteFile(path, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(tempDir, "symlink.txt")
	if err := os.Symlink(path, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	hardlink := filepath.Join(tempDir, "hardlink.txt")
	if err := os.Link(path, hardlink); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}

	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	for _, alias := range []string{path, symlink, hardlink} {
		unlock := patch.lockPath(alias)
		unlock()
	}

	created := filepath.Join(tempDir, "created.txt")
	unlock := patch.lockPath(created)
	if err := os.WriteFile(created, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock()
	createdHardlink := filepath.Join(tempDir, "created-hardlink.txt")
	if err := os.Link(created, createdHardlink); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}
	unlock = patch.lockPath(createdHardlink)
	unlock()

	patch.pathLocksMu.Lock()
	defer patch.pathLocksMu.Unlock()
	if len(patch.pathLocks) != 2 {
		t.Fatalf("aliases created %d path locks, want 2", len(patch.pathLocks))
	}
}

func TestPatchToolConcurrentSameFilePreservesIndependentEdits(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	path := filepath.Join(tempDir, "shared.txt")
	var original strings.Builder
	for i := range 40 {
		fmt.Fprintf(&original, "[token-%d]\n", i)
	}
	if err := os.WriteFile(path, []byte(original.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 40)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			<-start
			input, err := json.Marshal(PatchInput{
				Path: path,
				Patches: []PatchRequest{{
					Operation: "replace",
					OldText:   fmt.Sprintf("[token-%d]", i),
					NewText:   fmt.Sprintf("[done-%d]", i),
				}},
			})
			if err != nil {
				errs <- err
				return
			}
			if result := patch.Run(context.Background(), input); result.Error != nil {
				errs <- result.Error
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if !strings.Contains(string(content), fmt.Sprintf("[done-%d]", i)) {
			t.Errorf("concurrent edit %d was lost", i)
		}
	}
}

func TestPatchToolConcurrentSharedClipboardTransactions(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	errs := make(chan error, 40)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			name := fmt.Sprintf("value-%d", i)
			path := filepath.Join(tempDir, fmt.Sprintf("shared-clipboard-%d.txt", i))
			if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
				errs <- err
				return
			}
			input, err := json.Marshal(PatchInput{
				Path: path,
				Patches: []PatchRequest{
					{Operation: "replace", OldText: name, NewText: "updated", ToClipboard: "shared"},
					{Operation: "append_eof", FromClipboard: "shared"},
				},
			})
			if err != nil {
				errs <- err
				return
			}
			<-start
			if result := patch.Run(context.Background(), input); result.Error != nil {
				errs <- result.Error
				return
			}
			content, err := os.ReadFile(path)
			if err != nil {
				errs <- err
				return
			}
			if got, want := string(content), "updated"+name; got != want {
				errs <- fmt.Errorf("%s = %q, want %q", path, got, want)
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPatchToolConcurrentClipboardAccess(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	errs := make(chan error, 20)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			name := fmt.Sprintf("item-%d", i)
			source := filepath.Join(tempDir, name+"-source.txt")
			if err := os.WriteFile(source, []byte(name), 0o600); err != nil {
				errs <- err
				return
			}
			copyInput, err := json.Marshal(PatchInput{
				Path: source,
				Patches: []PatchRequest{{
					Operation:   "replace",
					OldText:     name,
					NewText:     "updated",
					ToClipboard: name,
				}},
			})
			if err != nil {
				errs <- err
				return
			}
			if result := patch.Run(context.Background(), copyInput); result.Error != nil {
				errs <- result.Error
				return
			}

			pasteInput, err := json.Marshal(PatchInput{
				Path: filepath.Join(tempDir, name+"-dest.txt"),
				Patches: []PatchRequest{{
					Operation:     "overwrite",
					FromClipboard: name,
				}},
			})
			if err != nil {
				errs <- err
				return
			}
			if result := patch.Run(context.Background(), pasteInput); result.Error != nil {
				errs <- result.Error
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPatchTool_BasicOperations(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	// Test overwrite operation (creates new file)
	testFile := filepath.Join(tempDir, "test.txt")
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "Hello World\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("overwrite failed: %v", result.Error)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "Hello World\n" {
		t.Errorf("expected 'Hello World\\n', got %q", string(content))
	}

	// Test replace operation
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "World",
		NewText:   "Patch",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("replace failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	if string(content) != "Hello Patch\n" {
		t.Errorf("expected 'Hello Patch\\n', got %q", string(content))
	}

	// Test append_eof operation
	input.Patches = []PatchRequest{{
		Operation: "append_eof",
		NewText:   "Appended line\n",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("append_eof failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	expected := "Hello Patch\nAppended line\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}

	// Test prepend_bof operation
	input.Patches = []PatchRequest{{
		Operation: "prepend_bof",
		NewText:   "Prepended line\n",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("prepend_bof failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	expected = "Prepended line\nHello Patch\nAppended line\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}
}

func TestPatchTool_ClipboardOperations(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "clipboard.txt")

	// Create initial content
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "function original() {\n    return 'original';\n}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial overwrite failed: %v", result.Error)
	}

	// Test toClipboard operation
	input.Patches = []PatchRequest{{
		Operation:   "replace",
		OldText:     "function original() {\n    return 'original';\n}",
		NewText:     "function renamed() {\n    return 'renamed';\n}",
		ToClipboard: "saved_func",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("toClipboard failed: %v", result.Error)
	}

	// Test fromClipboard operation
	input.Patches = []PatchRequest{{
		Operation:     "append_eof",
		FromClipboard: "saved_func",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("fromClipboard failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "function original()") {
		t.Error("clipboard content not restored properly")
	}
}

func TestPatchTool_IndentationAdjustment(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "indent.go")

	// Create file with tab indentation
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc main() {\n\tif true {\n\t\t// placeholder\n\t}\n}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial setup failed: %v", result.Error)
	}

	// Test indentation adjustment: convert spaces to tabs
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "// placeholder",
		NewText:   "    fmt.Println(\"hello\")\n    fmt.Println(\"world\")",
		Reindent: &Reindent{
			Strip: "    ",
			Add:   "\t\t",
		},
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("indentation adjustment failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	expected := "\t\tfmt.Println(\"hello\")\n\t\tfmt.Println(\"world\")"
	if !strings.Contains(string(content), expected) {
		t.Errorf("indentation not adjusted correctly, got:\n%s", string(content))
	}
}

func TestPatchTool_FuzzyMatching(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "fuzzy.go")

	// Create Go file with specific indentation
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc test() {\n\tif condition {\n\t\tfmt.Println(\"hello\")\n\t\tfmt.Println(\"world\")\n\t}\n}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial setup failed: %v", result.Error)
	}

	// Test fuzzy matching with different whitespace
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "if condition {\n        fmt.Println(\"hello\")\n        fmt.Println(\"world\")\n    }", // spaces instead of tabs
		NewText:   "if condition {\n\t\tfmt.Println(\"modified\")\n\t}",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("fuzzy matching failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "modified") {
		t.Error("fuzzy matching did not work")
	}
}

func TestPatchTool_ErrorCases(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "error.txt")

	// Test replace operation on non-existent file
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "replace",
			OldText:   "something",
			NewText:   "else",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error == nil {
		t.Error("expected error for replace on non-existent file")
	}

	// Create file with duplicate text
	input.Patches = []PatchRequest{{
		Operation: "overwrite",
		NewText:   "duplicate\nduplicate\n",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create test file: %v", result.Error)
	}

	// Test non-unique text
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "duplicate",
		NewText:   "unique",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not unique") {
		t.Error("expected non-unique error")
	}

	// Test missing text
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "nonexistent",
		NewText:   "something",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not found") {
		t.Error("expected not found error")
	}

	// Test invalid clipboard reference
	input.Patches = []PatchRequest{{
		Operation:     "append_eof",
		FromClipboard: "nonexistent",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "clipboard") {
		t.Error("expected clipboard error")
	}

	// Test missing patches field (simulates truncated LLM response)
	msg = json.RawMessage(`{"path":"server/dashboard.go"}`)
	result = patch.Run(ctx, msg)
	if result.Error == nil {
		t.Error("expected error for missing patches field")
	}
	if !strings.Contains(result.Error.Error(), "missing or empty") {
		t.Errorf("expected 'missing or empty' in error, got: %v", result.Error)
	}

	// Test empty patches array
	msg = json.RawMessage(`{"path":"server/dashboard.go","patches":[]}`)
	result = patch.Run(ctx, msg)
	if result.Error == nil {
		t.Error("expected error for empty patches array")
	}
}

func TestPatchTool_FlexibleInputParsing(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "flexible.txt")

	// Test single patch format (PatchInputOne)
	inputOne := PatchInputOne{
		Path: testFile,
		Patches: &PatchRequest{
			Operation: "overwrite",
			NewText:   "Single patch format\n",
		},
	}

	msg, _ := json.Marshal(inputOne)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("single patch format failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if string(content) != "Single patch format\n" {
		t.Error("single patch format did not work")
	}

	// Test string patch format (PatchInputOneString)
	patchStr := `{"operation": "replace", "oldText": "Single", "newText": "Modified"}`
	inputStr := PatchInputOneString{
		Path:    testFile,
		Patches: patchStr,
	}

	msg, _ = json.Marshal(inputStr)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("string patch format failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	if !strings.Contains(string(content), "Modified") {
		t.Error("string patch format did not work")
	}
}

func TestPatchTool_AutogeneratedDetection(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "generated.go")

	// Create autogenerated file
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "// Code generated by tool. DO NOT EDIT.\npackage main\n\nfunc generated() {}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create generated file: %v", result.Error)
	}

	// Test patching autogenerated file (should warn but work)
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "func generated() {}",
		NewText:   "func modified() {}",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("patching generated file failed: %v", result.Error)
	}

	if len(result.LLMContent) == 0 || !strings.Contains(result.LLMContent[0].Text, "autogenerated") {
		t.Error("expected autogenerated warning")
	}
}

func TestPatchTool_MultiplePatches(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "multi.go")
	var msg []byte
	var result llm.ToolOut

	// Apply multiple patches - first create file, then modify
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc first() {\n\tprintln(\"first\")\n}\n\nfunc second() {\n\tprintln(\"second\")\n}\n",
		}},
	}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create initial file: %v", result.Error)
	}

	// Now apply multiple patches in one call
	input.Patches = []PatchRequest{
		{
			Operation: "replace",
			OldText:   "println(\"first\")",
			NewText:   "println(\"ONE\")",
		},
		{
			Operation: "replace",
			OldText:   "println(\"second\")",
			NewText:   "println(\"TWO\")",
		},
		{
			Operation: "append_eof",
			NewText:   "\n// Multiple patches applied\n",
		},
	}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("multiple patches failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	contentStr := string(content)
	if !strings.Contains(contentStr, "ONE") || !strings.Contains(contentStr, "TWO") {
		t.Error("multiple patches not applied correctly")
	}
	if !strings.Contains(contentStr, "Multiple patches applied") {
		t.Error("append_eof in multiple patches not applied")
	}
}

func TestPatchTool_CopyRecipe(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "copy.txt")

	// Create initial content
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "original text",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create file: %v", result.Error)
	}

	// Test copy recipe (toClipboard + fromClipboard with same name)
	input.Patches = []PatchRequest{{
		Operation:     "replace",
		OldText:       "original text",
		NewText:       "replaced text",
		ToClipboard:   "copy_test",
		FromClipboard: "copy_test",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("copy recipe failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	// The copy recipe should preserve the original text
	if string(content) != "original text" {
		t.Errorf("copy recipe failed, expected 'original text', got %q", string(content))
	}
}

func TestPatchTool_RelativePaths(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	// Test relative path resolution
	input := PatchInput{
		Path: "relative.txt", // relative path
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "relative path test\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("relative path failed: %v", result.Error)
	}

	// Check file was created in correct location
	expectedPath := filepath.Join(tempDir, "relative.txt")
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not created at expected path: %v", err)
	}
	if string(content) != "relative path test\n" {
		t.Error("relative path file content incorrect")
	}
}

// Benchmark basic patch operations
func BenchmarkPatchTool_BasicOperations(b *testing.B) {
	tempDir := b.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "bench.go")
	initialContent := "package main\n\nfunc test() {\n\tfor i := 0; i < 100; i++ {\n\t\tfmt.Println(i)\n\t}\n}\n"

	// Setup
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   initialContent,
		}},
	}
	msg, _ := json.Marshal(input)
	patch.Run(ctx, msg)

	for b.Loop() {
		// Benchmark replace operation
		input.Patches = []PatchRequest{{
			Operation: "replace",
			OldText:   "fmt.Println(i)",
			NewText:   "fmt.Printf(\"%d\\n\", i)",
		}}

		msg, _ := json.Marshal(input)
		result := patch.Run(ctx, msg)
		if result.Error != nil {
			b.Fatalf("benchmark failed: %v", result.Error)
		}

		// Reset for next iteration
		input.Patches = []PatchRequest{{
			Operation: "replace",
			OldText:   "fmt.Printf(\"%d\\n\", i)",
			NewText:   "fmt.Println(i)",
		}}
		msg, _ = json.Marshal(input)
		patch.Run(ctx, msg)
	}
}

func TestPatchTool_CallbackFunction(t *testing.T) {
	tempDir := t.TempDir()
	callbackCalled := false
	var capturedInput PatchInput
	var capturedOutput llm.ToolOut

	patch := &PatchTool{
		WorkingDir: NewMutableWorkingDir(tempDir),
		Callback: func(input PatchInput, output llm.ToolOut) llm.ToolOut {
			callbackCalled = true
			capturedInput = input
			capturedOutput = output
			// Modify the output
			output.LLMContent = llm.TextContent("Modified by callback")
			return output
		},
	}

	ctx := context.Background()
	testFile := filepath.Join(tempDir, "callback.txt")

	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "callback test",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)

	if !callbackCalled {
		t.Error("callback was not called")
	}

	if capturedInput.Path != testFile {
		t.Error("callback did not receive correct input")
	}

	if len(result.LLMContent) == 0 || result.LLMContent[0].Text != "Modified by callback" {
		t.Error("callback did not modify output correctly")
	}

	if capturedOutput.Error != nil {
		t.Errorf("callback received error: %v", capturedOutput.Error)
	}
}

func TestPatchTool_DisplayDataContainsUnifiedDiffOnly(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "display.txt")
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "before\n",
		}},
	}

	msg, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("failed to marshal patch input: %v", err)
	}
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial overwrite failed: %v", result.Error)
	}

	input = PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "replace",
			OldText:   "before",
			NewText:   "after",
		}},
	}
	msg, err = json.Marshal(input)
	if err != nil {
		t.Fatalf("failed to marshal patch input: %v", err)
	}
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("replace failed: %v", result.Error)
	}

	display, ok := result.Display.(PatchDisplayData)
	if !ok {
		t.Fatalf("expected PatchDisplayData display payload, got %T", result.Display)
	}
	if display.Path != testFile {
		t.Fatalf("display path = %q, want %q", display.Path, testFile)
	}
	if display.Diff == "" {
		t.Fatal("display diff should not be empty")
	}
	if !strings.Contains(display.Diff, "@@") {
		t.Fatalf("display diff does not look like unified diff: %q", display.Diff)
	}

	displayJSON, err := json.Marshal(display)
	if err != nil {
		t.Fatalf("failed to marshal display payload: %v", err)
	}
	if strings.Contains(string(displayJSON), "oldContent") {
		t.Fatalf("display payload should not include oldContent: %s", string(displayJSON))
	}
	if strings.Contains(string(displayJSON), "newContent") {
		t.Fatalf("display payload should not include newContent: %s", string(displayJSON))
	}
}
