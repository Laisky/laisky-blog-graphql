// Package arch provides technical documentation for building modern TUI applications in Go.
//
// # Building Modern Terminal User Interfaces in Go with Bubble Tea (2025-2026)
//
// This document provides a comprehensive guide to transforming traditional Go command-line
// applications into modern, interactive Terminal User Interfaces (TUIs) using the Charm
// Bubble Tea framework.
//
// ## Overview
//
// Bubble Tea is a powerful TUI framework based on The Elm Architecture, providing a
// functional approach to building terminal applications. It's the industry standard
// for Go TUI development in 2025-2026.
//
// ## Installation
//
// Install the Charm libraries using go get:
//
//	go get github.com/charmbracelet/bubbletea@latest
//	go get github.com/charmbracelet/lipgloss@latest
//	go get github.com/charmbracelet/bubbles@latest
//
// ## Core Concepts
//
// ### The Elm Architecture (TEA)
//
// Bubble Tea follows The Elm Architecture pattern with three core concepts:
//
//  1. Model: The application state
//  2. Update: A function that handles messages and updates the model
//  3. View: A function that renders the model to a string
//
// ### Message-Based Updates
//
// Instead of directly mutating state, Bubble Tea uses messages to trigger state changes.
// This makes the application more predictable and easier to debug.
//
// ## Basic Example
//
// Here's a minimal example of a Bubble Tea application:
package arch

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// =============================================================================
// SECTION 1: Basic Model Structure
// =============================================================================

// Model represents the application state. In Bubble Tea, all state is contained
// in a single Model struct that implements the tea.Model interface.
type Model struct {
	// cursor tracks the current position in a list or menu
	cursor int
	// choices represents menu items
	choices []string
	// selected tracks which items have been selected
	selected map[int]struct{}
	// quitting indicates if the user is exiting the application
	quitting bool
}

// NewModel creates a new model with initial state.
// This is the constructor pattern recommended for Bubble Tea applications.
func NewModel() Model {
	return Model{
		choices:  []string{"Option 1", "Option 2", "Option 3"},
		selected: make(map[int]struct{}),
	}
}

// Init implements tea.Model. It returns an initial command to run.
// Return nil if no initial command is needed.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model. It handles messages and returns updated model.
// This is the core of the Elm Architecture - all state changes happen here.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}
		case "enter", " ":
			if _, ok := m.selected[m.cursor]; ok {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
			}
		}
	}
	return m, nil
}

// View implements tea.Model. It renders the model as a string.
// The string is the entire terminal UI - Bubble Tea handles the rendering.
func (m Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	s := "Select options:\n\n"

	for i, choice := range m.choices {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		checked := " "
		if _, ok := m.selected[i]; ok {
			checked = "x"
		}

		s += fmt.Sprintf("%s [%s] %s\n", cursor, checked, choice)
	}

	s += "\nPress q to quit.\n"
	return s
}

// =============================================================================
// SECTION 2: Styling with Lipgloss
// =============================================================================

// Lipgloss provides CSS-like styling for terminal applications.
// Define styles as package-level variables for reuse.

// Color palette - using Catppuccin Mocha theme as example
var (
	primaryColor   = lipgloss.Color("#7C3AED") // Violet
	secondaryColor = lipgloss.Color("#10B981") // Emerald
	accentColor    = lipgloss.Color("#F59E0B") // Amber
	errorColor     = lipgloss.Color("#EF4444") // Red
	successColor   = lipgloss.Color("#22C55E") // Green
	mutedColor     = lipgloss.Color("#6C7086") // Muted text
	borderColor    = lipgloss.Color("#45475A") // Border
)

// TitleStyle demonstrates how to create a styled title
var TitleStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(primaryColor).
	MarginBottom(1).
	Padding(0, 1)

// BoxStyle creates a bordered container
var BoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(borderColor).
	Padding(1, 2)

// SuccessStyle for success messages
var SuccessStyle = lipgloss.NewStyle().
	Foreground(successColor).
	Bold(true)

// ErrorStyle for error messages
var ErrorStyle = lipgloss.NewStyle().
	Foreground(errorColor).
	Bold(true)

// HelpStyle for help text
var HelpStyle = lipgloss.NewStyle().
	Foreground(mutedColor).
	MarginTop(1)

// =============================================================================
// SECTION 3: Using Built-in Components (Bubbles)
// =============================================================================

// AdvancedModel demonstrates using multiple Bubble Tea components
type AdvancedModel struct {
	// State management
	state ViewState

	// Built-in components from bubbles package
	menuList list.Model        // List selection component
	inputs   []textinput.Model // Text input fields
	spinner  spinner.Model     // Loading spinner

	// Focus management for multiple inputs
	focusIndex int

	// Window dimensions for responsive layouts
	width  int
	height int
}

// ViewState represents different views/screens in the application
type ViewState int

const (
	ViewMain ViewState = iota
	ViewForm
	ViewLoading
	ViewResult
)

// MenuItem implements list.Item interface for custom menu items
type MenuItem struct {
	title       string
	description string
	command     string
}

// Title returns the item title (implements list.Item)
func (i MenuItem) Title() string { return i.title }

// Description returns the item description (implements list.Item)
func (i MenuItem) Description() string { return i.description }

// FilterValue returns the value to filter on (implements list.Item)
func (i MenuItem) FilterValue() string { return i.title }

// NewAdvancedModel creates a model with multiple components
func NewAdvancedModel() AdvancedModel {
	// Create menu items
	items := []list.Item{
		MenuItem{title: "🚀 Start Server", description: "Launch the API server", command: "api"},
		MenuItem{title: "📥 Import Data", description: "Import data from files", command: "import"},
		MenuItem{title: "🔄 Migrate", description: "Run database migrations", command: "migrate"},
	}

	// Create list with custom delegate styling
	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(primaryColor).
		BorderForeground(primaryColor)

	menuList := list.New(items, delegate, 0, 0)
	menuList.Title = "My Application"
	menuList.SetShowStatusBar(false)
	menuList.SetFilteringEnabled(false)

	// Create spinner
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accentColor)

	return AdvancedModel{
		state:    ViewMain,
		menuList: menuList,
		spinner:  sp,
	}
}

// createInputs demonstrates creating multiple text input fields
func createInputs() []textinput.Model {
	inputs := make([]textinput.Model, 2)

	// First input - file path
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "Enter file path..."
	inputs[0].Focus()
	inputs[0].CharLimit = 256
	inputs[0].Width = 50
	inputs[0].Prompt = "📁 "
	inputs[0].PromptStyle = lipgloss.NewStyle().Foreground(secondaryColor)

	// Second input - configuration
	inputs[1] = textinput.New()
	inputs[1].Placeholder = "Enter configuration..."
	inputs[1].CharLimit = 128
	inputs[1].Width = 50
	inputs[1].Prompt = "⚙️  "
	inputs[1].PromptStyle = lipgloss.NewStyle().Foreground(secondaryColor)

	return inputs
}

// =============================================================================
// SECTION 4: Key Bindings
// =============================================================================

// keyMap defines application key bindings
type keyMap struct {
	Up    key.Binding
	Down  key.Binding
	Enter key.Binding
	Back  key.Binding
	Tab   key.Binding
	Quit  key.Binding
}

// DefaultKeyMap returns the default key bindings
func DefaultKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "move down"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "go back"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next field"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// =============================================================================
// SECTION 5: Responsive Design
// =============================================================================

// Init implements tea.Model with spinner initialization
func (m AdvancedModel) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update handles window resize messages for responsive design
func (m AdvancedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Handle window resize
		m.width = msg.Width
		m.height = msg.Height
		// Adjust list size based on window
		m.menuList.SetSize(msg.Width-4, msg.Height-6)
		return m, nil

	case tea.KeyMsg:
		keys := DefaultKeyMap()
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Enter):
			// Handle selection
			return m, nil
		case key.Matches(msg, keys.Back):
			if m.state != ViewMain {
				m.state = ViewMain
			}
			return m, nil
		}

	case spinner.TickMsg:
		if m.state == ViewLoading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	// Update the list component
	var cmd tea.Cmd
	m.menuList, cmd = m.menuList.Update(msg)
	return m, cmd
}

// View renders the appropriate view based on state
func (m AdvancedModel) View() string {
	switch m.state {
	case ViewMain:
		return m.renderMainMenu()
	case ViewForm:
		return m.renderForm()
	case ViewLoading:
		return m.renderLoading()
	case ViewResult:
		return m.renderResult()
	default:
		return "Unknown state"
	}
}

func (m AdvancedModel) renderMainMenu() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.menuList.View(),
		HelpStyle.Render("↑/↓ navigate • enter select • q quit"),
	)
}

func (m AdvancedModel) renderForm() string {
	return BoxStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			TitleStyle.Render("Configuration"),
			"Fill in the required fields:",
			"", // spacer
			HelpStyle.Render("tab: next field • enter: submit • esc: back"),
		),
	)
}

func (m AdvancedModel) renderLoading() string {
	return BoxStyle.Render(
		lipgloss.JoinVertical(lipgloss.Center,
			m.spinner.View()+" Loading...",
			HelpStyle.Render("Please wait..."),
		),
	)
}

func (m AdvancedModel) renderResult() string {
	return BoxStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			SuccessStyle.Render("✅ Operation completed successfully!"),
			"",
			HelpStyle.Render("enter/esc: back to menu"),
		),
	)
}

// =============================================================================
// SECTION 6: Integration with Cobra Commands
// =============================================================================

// The TUI can be integrated with existing Cobra commands as a new subcommand.
// Here's how to add a TUI command to an existing Cobra application:

/*
Example Cobra command integration:

package cmd

import (
    "github.com/spf13/cobra"
    tea "github.com/charmbracelet/bubbletea"
    "your-project/tui"
)

var tuiCmd = &cobra.Command{
    Use:   "tui",
    Short: "Launch interactive TUI",
    Long:  `Launch an interactive Terminal User Interface.`,
    Run: func(cmd *cobra.Command, args []string) {
        model := tui.NewModel()
        p := tea.NewProgram(
            model,
            tea.WithAltScreen(),       // Use alternate screen buffer
            tea.WithMouseCellMotion(), // Enable mouse support
        )
        if _, err := p.Run(); err != nil {
            fmt.Fprintln(os.Stderr, "Error:", err)
            os.Exit(1)
        }
    },
}

func init() {
    rootCmd.AddCommand(tuiCmd)
}
*/

// =============================================================================
// SECTION 7: Running the Application
// =============================================================================

// RunTUI demonstrates how to start a Bubble Tea program
func RunTUI() error {
	model := NewModel()

	// Create program with options
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),       // Use alternate screen (recommended)
		tea.WithMouseCellMotion(), // Enable mouse support
	)

	// Run the program
	_, err := p.Run()
	return err
}

// Main demonstrates a complete TUI application entry point
func Main() {
	if err := RunTUI(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

// =============================================================================
// SECTION 8: Best Practices
// =============================================================================

/*
Best Practices for Bubble Tea Applications:

1. State Management
   - Keep all state in the Model struct
   - Never mutate state directly; always return new model from Update
   - Use pointer receivers only when necessary for large structs

2. Component Organization
   - Separate styles into their own file (styles.go)
   - Group related components in packages
   - Use meaningful ViewState constants

3. Responsive Design
   - Always handle tea.WindowSizeMsg
   - Calculate component sizes based on window dimensions
   - Test with different terminal sizes

4. Error Handling
   - Return errors as custom messages
   - Display errors in the UI gracefully
   - Log errors for debugging

5. Performance
   - Minimize allocations in Update and View
   - Use string builders for complex views
   - Debounce rapid input events if needed

6. Testing
   - Test Update function with various messages
   - Verify View output for different states
   - Use golden tests for complex UIs

7. Accessibility
   - Support both vim-style (hjkl) and arrow key navigation
   - Provide clear feedback for actions
   - Use high-contrast color schemes
*/

// =============================================================================
// SECTION 9: Project Structure
// =============================================================================

/*
Recommended project structure for a TUI application:

project/
├── cmd/
│   ├── root.go          # Main Cobra command
│   ├── api.go           # API server command
│   ├── tui.go           # TUI launcher command
│   └── tui/             # TUI package
│       ├── model.go     # Main model and logic
│       ├── styles.go    # Lipgloss styles
│       ├── views.go     # View rendering functions
│       └── keys.go      # Key bindings
├── internal/
│   └── ...              # Business logic
├── main.go
├── go.mod
└── go.sum
*/

// =============================================================================
// SECTION 10: Advanced Topics
// =============================================================================

/*
Advanced Topics:

1. Custom Commands
   - tea.Cmd is a function that returns tea.Msg
   - Use commands for async operations
   - Example: HTTP requests, file I/O, timers

2. Subscriptions
   - Use tea.Sub for long-running background tasks
   - Good for: file watchers, websockets, timers

3. Sub-Models
   - Compose complex UIs from smaller models
   - Each sub-model can have its own Update and View

4. Batch Commands
   - Use tea.Batch to run multiple commands concurrently
   - Useful for parallel initialization

5. Sequences
   - Use tea.Sequence for ordered command execution
   - Each command runs after the previous completes
*/

// Example of a custom command for async operations
type dataLoadedMsg struct {
	data string
	err  error
}

func loadData() tea.Cmd {
	return func() tea.Msg {
		// Simulate async operation
		// In practice, this could be an HTTP request, database query, etc.
		return dataLoadedMsg{data: "loaded data", err: nil}
	}
}

// handleDataLoaded demonstrates handling async results in Update
func handleDataLoaded(msg dataLoadedMsg, m Model) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// Handle error
		return m, nil
	}
	// Process data
	return m, nil
}

// Ensure unused imports are used
var (
	_ = createInputs
	_ = loadData
	_ = handleDataLoaded
)
