// Package cmd command line
package cmd

import (
	"fmt"
	"os"

	"github.com/Laisky/laisky-blog-graphql/cmd/tui"

	errors "github.com/Laisky/errors/v2"
	gcmd "github.com/Laisky/go-utils/v6/cmd"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var tuiCMD = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive TUI",
	Long: `Launch an interactive Terminal User Interface (TUI) for laisky-blog-graphql.

The TUI provides a modern, menu-driven interface for:
  • Starting the API server with custom configuration
  • Importing comments from Disqus exports
  • Running database migrations
  • And more...

The interface uses the Charm Bubble Tea framework for a beautiful,
responsive terminal experience.

Example:
  go run main.go tui

Keyboard shortcuts:
  ↑/↓ or j/k  Navigate menu items
  Enter       Select / Confirm
  Tab         Next input field
  Esc         Go back
  q           Quit`,
	Args: gcmd.NoExtraArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := runTUI(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCMD.AddCommand(tuiCMD)
}

// runTUI starts the interactive Terminal User Interface and returns any start/run error.
func runTUI() error {
	model := tui.NewModel()

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),       // Use alternate screen buffer
		tea.WithMouseCellMotion(), // Enable mouse support
	)

	_, err := p.Run()
	return errors.WithStack(err)
}
