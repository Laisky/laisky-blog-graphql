# Building Modern Terminal User Interfaces (TUI) in Go with Bubble Tea

This guide explains how to transform a traditional Go command-line (cmd) program into a modern, interactive Terminal User Interface (TUI) using the [Charm Bubble Tea](https://github.com/charmbracelet/bubbletea) framework. It covers installation, architecture, code samples, and best practices for 2025–2026.

## Menu

- [Building Modern Terminal User Interfaces (TUI) in Go with Bubble Tea](#building-modern-terminal-user-interfaces-tui-in-go-with-bubble-tea)
  - [Menu](#menu)
  - [Why Bubble Tea?](#why-bubble-tea)
  - [1. Installation](#1-installation)
  - [2. Project Structure](#2-project-structure)
  - [3. Core Concepts](#3-core-concepts)
    - [The Elm Architecture (TEA)](#the-elm-architecture-tea)
    - [Message-Driven](#message-driven)
  - [4. Minimal Example](#4-minimal-example)
  - [5. Styling with Lipgloss](#5-styling-with-lipgloss)
  - [6. Using Bubbles Components](#6-using-bubbles-components)
  - [7. Key Bindings](#7-key-bindings)
  - [8. Integrating with Cobra](#8-integrating-with-cobra)
  - [9. Best Practices](#9-best-practices)
  - [10. Advanced Topics](#10-advanced-topics)
  - [11. Full Example Project](#11-full-example-project)
  - [References](#references)

## Why Bubble Tea?

- **Industry standard** for Go TUIs (2025–2026)
- Functional, message-driven architecture (The Elm Architecture)
- Beautiful, responsive, and accessible terminal UIs
- Rich ecosystem: [Lipgloss](https://github.com/charmbracelet/lipgloss) for styling, [Bubbles](https://github.com/charmbracelet/bubbles) for components

## 1. Installation

```sh
# Install Bubble Tea and friends
 go get github.com/charmbracelet/bubbletea@latest
 go get github.com/charmbracelet/lipgloss@latest
 go get github.com/charmbracelet/bubbles@latest
```

## 2. Project Structure

```
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
```

## 3. Core Concepts

### The Elm Architecture (TEA)

- **Model**: All application state
- **Update**: Handles messages, returns new model
- **View**: Renders the model as a string

### Message-Driven

- All state changes are triggered by messages (e.g., key presses, async results)
- Predictable, testable, and easy to debug

## 4. Minimal Example

```go
package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	cursor int
	choices []string
	selected map[int]struct{}
	quitting bool
}

func initialModel() model {
	return model{
		choices:  []string{"Option 1", "Option 2", "Option 3"},
		selected: make(map[int]struct{}),
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 { m.cursor-- }
		case "down", "j":
			if m.cursor < len(m.choices)-1 { m.cursor++ }
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

func (m model) View() string {
	if m.quitting { return "Goodbye!\n" }
	s := "Select options:\n\n"
	for i, choice := range m.choices {
		cursor := " "; if m.cursor == i { cursor = ">" }
		checked := " "; if _, ok := m.selected[i]; ok { checked = "x" }
		s += fmt.Sprintf("%s [%s] %s\n", cursor, checked, choice)
	}
	s += "\nPress q to quit.\n"
	return s
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", err)
	}
}
```

## 5. Styling with Lipgloss

```go
import "github.com/charmbracelet/lipgloss"

var titleStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#7C3AED")).
	MarginBottom(1)

fmt.Println(titleStyle.Render("My TUI App"))
```

## 6. Using Bubbles Components

- [list](https://github.com/charmbracelet/bubbles/tree/master/list): Menus, selection
- [textinput](https://github.com/charmbracelet/bubbles/tree/master/textinput): Input fields
- [spinner](https://github.com/charmbracelet/bubbles/tree/master/spinner): Loading indicators

Example:

```go
import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/spinner"
)

// ...in your model struct:
menuList list.Model
inputs   []textinput.Model
spinner  spinner.Model
```

## 7. Key Bindings

```go
import "github.com/charmbracelet/bubbles/key"

var keys = struct {
	Up, Down, Enter, Back, Tab, Quit key.Binding
}{
	Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Tab:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
	Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}
```

## 8. Integrating with Cobra

Add a TUI command to your Cobra CLI:

```go
import (
	"github.com/spf13/cobra"
	tea "github.com/charmbracelet/bubbletea"
	"your-project/cmd/tui"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive TUI",
	Run: func(cmd *cobra.Command, args []string) {
		model := tui.NewModel()
		p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
```

## 9. Best Practices

- **Keep all state in the Model struct**
- **Never mutate state directly**; always return a new model from Update
- **Handle tea.WindowSizeMsg** for responsive layouts
- **Use Lipgloss for consistent styling**
- **Test Update and View functions**
- **Support both vim-style (hjkl) and arrow key navigation**
- **Display errors and loading states gracefully**

## 10. Advanced Topics

- **Async operations**: Use `tea.Cmd` for background tasks (e.g., HTTP requests)
- **Sub-models**: Compose complex UIs from smaller models
- **Batch commands**: Use `tea.Batch` to run multiple commands concurrently
- **Accessibility**: Use high-contrast color schemes, clear feedback, and keyboard shortcuts

## 11. Full Example Project

See the `cmd/tui/` directory in this repository for a full-featured, production-grade TUI implementation with:

- Menu navigation
- Input forms
- Loading spinners
- Result display
- Modern color palette
- Integration with existing CLI commands

## References

- [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- [Lipgloss](https://github.com/charmbracelet/lipgloss)
- [Bubbles](https://github.com/charmbracelet/bubbles)
- [Charmbracelet TUI Cookbook](https://charm.sh)
