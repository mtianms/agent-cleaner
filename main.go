package main

import (
	"fmt"
	"os"
	"runtime"

	tea "charm.land/bubbletea/v2"
)

func main() {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "Agent Cleaner currently supports Windows only.")
		os.Exit(1)
	}

	service, err := newCleaner()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not initialize Agent Cleaner:", err)
		os.Exit(1)
	}

	program := tea.NewProgram(newModel(service))
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Agent Cleaner stopped:", err)
		os.Exit(1)
	}
}
