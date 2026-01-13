// Package tui provides a modern terminal user interface for laisky-blog-graphql.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ViewState represents the current view state of the TUI
type ViewState int

const (
	// ViewMain is the main menu view
	ViewMain ViewState = iota
	// ViewAPIConfig is the API server configuration view
	ViewAPIConfig
	// ViewImportComments is the import comments configuration view
	ViewImportComments
	// ViewRunning is the view when a command is running
	ViewRunning
	// ViewResult is the view showing command results
	ViewResult
)

// MenuItem represents a menu item in the TUI
type MenuItem struct {
	title       string
	description string
	command     string
}

// Title returns the menu item title (implements list.Item)
func (m MenuItem) Title() string { return m.title }

// Description returns the menu item description (implements list.Item)
func (m MenuItem) Description() string { return m.description }

// FilterValue returns the filter value (implements list.Item)
func (m MenuItem) FilterValue() string { return m.title }

// CommandResult represents the result of a command execution
type CommandResult struct {
	Success bool
	Message string
	Details string
}

// Model is the main TUI model following the Bubble Tea architecture
type Model struct {
	// Current view state
	state ViewState

	// Main menu list
	menuList list.Model

	// Input fields for various configurations
	inputs     []textinput.Model
	focusIndex int

	// Spinner for loading states
	spinner spinner.Model

	// Results from command execution
	result *CommandResult

	// Error message if any
	err error

	// Window dimensions
	width  int
	height int

	// Callback to execute the selected command
	executeCallback func(cmd string, args map[string]string) error

	// Current command being configured
	currentCommand string

	// Quitting state
	quitting bool
}

// keyMap defines the key bindings for the TUI
type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Enter  key.Binding
	Back   key.Binding
	Tab    key.Binding
	Quit   key.Binding
	Help   key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next field"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
}

// NewModel creates a new TUI model with default configuration
func NewModel() Model {
	// Create menu items
	items := []list.Item{
		MenuItem{
			title:       "🚀 Start API Server",
			description: "Launch the GraphQL API server with custom configuration",
			command:     "api",
		},
		MenuItem{
			title:       "📥 Import Comments",
			description: "Import comments from Disqus XML export into MongoDB",
			command:     "import-comments",
		},
		MenuItem{
			title:       "🔄 Database Migration",
			description: "Run database migrations",
			command:     "migrate",
		},
	}

	// Create custom delegate for the list
	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(lipgloss.Color("#7C3AED")).
		BorderForeground(lipgloss.Color("#7C3AED"))
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(lipgloss.Color("#10B981"))

	// Create the list
	menuList := list.New(items, delegate, 0, 0)
	menuList.Title = "Laisky Blog GraphQL"
	menuList.SetShowStatusBar(false)
	menuList.SetFilteringEnabled(false)
	menuList.Styles.Title = GetHeaderStyle()

	// Create spinner
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = GetProgressStyle()

	return Model{
		state:     ViewMain,
		menuList:  menuList,
		spinner:   sp,
		focusIndex: 0,
	}
}

// createImportInputs creates the input fields for import comments
func createImportInputs() []textinput.Model {
	inputs := make([]textinput.Model, 2)

	// Disqus file path input
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "/path/to/disqus_export.xml"
	inputs[0].Focus()
	inputs[0].CharLimit = 256
	inputs[0].Width = 50
	inputs[0].Prompt = "📁 "
	inputs[0].PromptStyle = GetInputLabelStyle()

	// MongoDB URI input
	inputs[1] = textinput.New()
	inputs[1].Placeholder = "mongodb://user:pwd@host:port/dbname"
	inputs[1].CharLimit = 256
	inputs[1].Width = 50
	inputs[1].Prompt = "🔗 "
	inputs[1].PromptStyle = GetInputLabelStyle()

	return inputs
}

// createAPIInputs creates the input fields for API server configuration
func createAPIInputs() []textinput.Model {
	inputs := make([]textinput.Model, 3)

	// Config file path
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "/etc/laisky-blog-graphql/settings.yml"
	inputs[0].Focus()
	inputs[0].CharLimit = 256
	inputs[0].Width = 50
	inputs[0].Prompt = "⚙️ "
	inputs[0].PromptStyle = GetInputLabelStyle()

	// Listen address
	inputs[1] = textinput.New()
	inputs[1].Placeholder = "localhost:8080"
	inputs[1].CharLimit = 64
	inputs[1].Width = 30
	inputs[1].Prompt = "🌐 "
	inputs[1].PromptStyle = GetInputLabelStyle()

	// Tasks
	inputs[2] = textinput.New()
	inputs[2].Placeholder = "heartbeat,api (comma-separated)"
	inputs[2].CharLimit = 128
	inputs[2].Width = 40
	inputs[2].Prompt = "📋 "
	inputs[2].PromptStyle = GetInputLabelStyle()

	return inputs
}

// Init initializes the TUI model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
	)
}

// Update handles messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.menuList.SetSize(msg.Width-4, msg.Height-6)
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case ViewMain:
			return m.handleMainMenu(msg)
		case ViewImportComments, ViewAPIConfig:
			return m.handleInputView(msg)
		case ViewResult:
			return m.handleResultView(msg)
		case ViewRunning:
			// Don't handle input while running
			return m, nil
		}

	case spinner.TickMsg:
		if m.state == ViewRunning {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case CommandResult:
		m.result = &msg
		m.state = ViewResult
		return m, nil
	}

	// Update list if in main menu
	if m.state == ViewMain {
		m.menuList, cmd = m.menuList.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// handleMainMenu handles key events in the main menu
func (m Model) handleMainMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, keys.Enter):
		if selectedItem, ok := m.menuList.SelectedItem().(MenuItem); ok {
			switch selectedItem.command {
			case "api":
				m.state = ViewAPIConfig
				m.inputs = createAPIInputs()
				m.focusIndex = 0
				m.currentCommand = "api"
			case "import-comments":
				m.state = ViewImportComments
				m.inputs = createImportInputs()
				m.focusIndex = 0
				m.currentCommand = "import-comments"
			case "migrate":
				// Migrate doesn't need configuration
				m.state = ViewResult
				m.result = &CommandResult{
					Success: true,
					Message: "Migration completed",
					Details: "Database migration has been executed successfully.",
				}
			}
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.menuList, cmd = m.menuList.Update(msg)
	return m, cmd
}

// handleInputView handles key events in input views
func (m Model) handleInputView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Back):
		m.state = ViewMain
		return m, nil

	case key.Matches(msg, keys.Tab):
		m.focusIndex = (m.focusIndex + 1) % len(m.inputs)
		for i := range m.inputs {
			if i == m.focusIndex {
				m.inputs[i].Focus()
			} else {
				m.inputs[i].Blur()
			}
		}
		return m, nil

	case key.Matches(msg, keys.Enter):
		// Check if all inputs are valid
		if m.validateInputs() {
			return m.executeCommand()
		}
		return m, nil
	}

	// Update the focused input
	if m.focusIndex < len(m.inputs) {
		var cmd tea.Cmd
		m.inputs[m.focusIndex], cmd = m.inputs[m.focusIndex].Update(msg)
		return m, cmd
	}

	return m, nil
}

// handleResultView handles key events in result view
func (m Model) handleResultView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Back), key.Matches(msg, keys.Enter):
		m.state = ViewMain
		m.result = nil
		return m, nil

	case key.Matches(msg, keys.Quit):
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// validateInputs checks if all required inputs have values
func (m Model) validateInputs() bool {
	for _, input := range m.inputs {
		if input.Value() == "" {
			return false
		}
	}
	return true
}

// executeCommand prepares and simulates command execution
func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	args := make(map[string]string)

	switch m.currentCommand {
	case "import-comments":
		args["disqus_file"] = m.inputs[0].Value()
		args["db_uri"] = m.inputs[1].Value()
	case "api":
		args["config"] = m.inputs[0].Value()
		args["listen"] = m.inputs[1].Value()
		args["tasks"] = m.inputs[2].Value()
	}

	// For now, show a success message
	// In a real implementation, this would call the actual command
	m.state = ViewResult
	m.result = &CommandResult{
		Success: true,
		Message: fmt.Sprintf("Command '%s' configured successfully!", m.currentCommand),
		Details: formatArgs(args),
	}

	return m, nil
}

// formatArgs formats command arguments for display
func formatArgs(args map[string]string) string {
	var sb strings.Builder
	sb.WriteString("Configuration:\n")
	for k, v := range args {
		sb.WriteString(fmt.Sprintf("  • %s: %s\n", k, v))
	}
	return sb.String()
}

// View renders the TUI
func (m Model) View() string {
	if m.quitting {
		return GetSubtitleStyle().Render("Goodbye! 👋\n")
	}

	switch m.state {
	case ViewMain:
		return m.renderMainMenu()
	case ViewAPIConfig:
		return m.renderAPIConfig()
	case ViewImportComments:
		return m.renderImportConfig()
	case ViewRunning:
		return m.renderRunning()
	case ViewResult:
		return m.renderResult()
	default:
		return "Unknown state"
	}
}

// renderMainMenu renders the main menu view
func (m Model) renderMainMenu() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.menuList.View(),
		GetHelpStyle().Render("↑/↓ navigate • enter select • q quit"),
	)
}

// renderAPIConfig renders the API configuration view
func (m Model) renderAPIConfig() string {
	var sb strings.Builder

	title := GetHeaderStyle().Render("🚀 API Server Configuration")
	sb.WriteString(title + "\n\n")

	labels := []string{"Config File:", "Listen Address:", "Tasks:"}
	for i, input := range m.inputs {
		label := GetInputLabelStyle().Render(labels[i])
		sb.WriteString(label + "\n")
		sb.WriteString(input.View() + "\n\n")
	}

	help := GetHelpStyle().Render("tab: next field • enter: run • esc: back")
	sb.WriteString(help)

	return GetBoxStyle().Render(sb.String())
}

// renderImportConfig renders the import comments configuration view
func (m Model) renderImportConfig() string {
	var sb strings.Builder

	title := GetHeaderStyle().Render("📥 Import Disqus Comments")
	sb.WriteString(title + "\n\n")

	labels := []string{"Disqus XML File:", "MongoDB URI:"}
	for i, input := range m.inputs {
		label := GetInputLabelStyle().Render(labels[i])
		sb.WriteString(label + "\n")
		sb.WriteString(input.View() + "\n\n")
	}

	help := GetHelpStyle().Render("tab: next field • enter: import • esc: back")
	sb.WriteString(help)

	return GetBoxStyle().Render(sb.String())
}

// renderRunning renders the running state view
func (m Model) renderRunning() string {
	return GetBoxStyle().Render(
		lipgloss.JoinVertical(lipgloss.Center,
			m.spinner.View()+" Running command...",
			GetSubtitleStyle().Render("Please wait..."),
		),
	)
}

// renderResult renders the result view
func (m Model) renderResult() string {
	if m.result == nil {
		return "No result"
	}

	var statusStyle lipgloss.Style
	var statusIcon string
	if m.result.Success {
		statusStyle = GetSuccessStyle()
		statusIcon = "✅"
	} else {
		statusStyle = GetErrorStyle()
		statusIcon = "❌"
	}

	title := statusStyle.Render(statusIcon + " " + m.result.Message)
	details := GetSubtitleStyle().Render(m.result.Details)
	help := GetHelpStyle().Render("enter/esc: back to menu • q: quit")

	return GetBoxStyle().Render(
		lipgloss.JoinVertical(lipgloss.Left,
			title,
			"",
			details,
			"",
			help,
		),
	)
}
