package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"shelley.exe.dev/committour"
)

func runTour(args []string) {
	if len(args) == 0 {
		tourUsage()
		os.Exit(1)
	}

	fs := flag.NewFlagSet("tour "+args[0], flag.ExitOnError)
	dir := fs.String("C", ".", "Git repository directory")
	fs.Parse(args[1:])
	rest := fs.Args()

	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch args[0] {
	case "chunks":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: shelley tour chunks [-C dir] <commit>")
			os.Exit(1)
		}
		hash, fragments, err := committour.Chunks(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		chunks := make([]committour.TourChunk, len(fragments))
		for i, fragment := range fragments {
			chunks[i].Patch = fragment
		}
		out, err := json.MarshalIndent(struct {
			Hash   string                 `json:"hash"`
			Chunks []committour.TourChunk `json:"chunks"`
		}{hash, chunks}, "", "  ")
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s\n", out)

	case "verify", "attach":
		if len(rest) != 2 {
			fmt.Fprintf(os.Stderr, "Usage: shelley tour %s [-C dir] <commit> <tour.json>\n", args[0])
			os.Exit(1)
		}
		data, err := os.ReadFile(rest[1])
		if err != nil {
			fail(err)
		}
		tour, err := committour.ParseTour(data)
		if err != nil {
			fail(err)
		}
		warnings, err := committour.Verify(*dir, rest[0], tour)
		for _, warning := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
		if err != nil {
			fail(err)
		}
		if args[0] == "verify" {
			fmt.Printf("tour for %s verifies\n", rest[0])
			return
		}
		if err := committour.WriteNote(*dir, rest[0], data); err != nil {
			fail(err)
		}
		fmt.Printf("attached tour note to %s\n", rest[0])

	case "show":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: shelley tour show [-C dir] <commit>")
			os.Exit(1)
		}
		data, err := committour.ReadNote(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		if _, err := os.Stdout.Write(data); err != nil {
			fail(err)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown tour subcommand: %s\n", args[0])
		tourUsage()
		os.Exit(1)
	}
}

func tourUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  shelley tour chunks [-C dir] <commit>              Print suggested patch fragments as JSON")
	fmt.Fprintln(os.Stderr, "  shelley tour verify [-C dir] <commit> <tour.json>  Verify a tour against the commit tree")
	fmt.Fprintln(os.Stderr, "  shelley tour attach [-C dir] <commit> <tour.json>  Verify, then store the tour as a git note")
	fmt.Fprintln(os.Stderr, "  shelley tour show [-C dir] <commit>                Print the stored tour note")
}
