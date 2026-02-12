// Package tui provides a modern terminal user interface for laisky-blog-graphql.
// It uses the Charm Bubble Tea framework to create an interactive menu-driven interface.
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Color palette for the TUI based on modern design principles
var (
	// Primary colors
	primaryColor   = lipgloss.Color("#7C3AED") // Violet
	secondaryColor = lipgloss.Color("#10B981") // Emerald
	accentColor    = lipgloss.Color("#F59E0B") // Amber
	errorColor     = lipgloss.Color("#EF4444") // Red
	successColor   = lipgloss.Color("#22C55E") // Green

	// Neutral colors
	bgColor     = lipgloss.Color("#1E1E2E") // Dark background
	fgColor     = lipgloss.Color("#CDD6F4") // Light foreground
	mutedColor  = lipgloss.Color("#6C7086") // Muted text
	borderColor = lipgloss.Color("#45475A") // Border
	selectedBg  = lipgloss.Color("#313244") // Selected background
	highlightBg = lipgloss.Color("#45475A") // Highlight background
)

// titleStyle creates the main title style
var titleStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(primaryColor).
	MarginBottom(1).
	Padding(0, 1)

// subtitleStyle creates the subtitle/description style
var subtitleStyle = lipgloss.NewStyle().
	Foreground(mutedColor).
	Italic(true)

// menuItemStyle creates the style for unselected menu items
var menuItemStyle = lipgloss.NewStyle().
	Foreground(fgColor).
	PaddingLeft(2)

// selectedMenuItemStyle creates the style for selected menu items
var selectedMenuItemStyle = lipgloss.NewStyle().
	Foreground(secondaryColor).
	Bold(true).
	Background(selectedBg).
	PaddingLeft(1)

// cursorStyle creates the style for the selection cursor
var cursorStyle = lipgloss.NewStyle().
	Foreground(accentColor).
	Bold(true)

// helpStyle creates the style for help text at the bottom
var helpStyle = lipgloss.NewStyle().
	Foreground(mutedColor).
	MarginTop(1)

// boxStyle creates a bordered box style
var boxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(borderColor).
	Padding(1, 2)

// successStyle creates style for success messages
var successStyle = lipgloss.NewStyle().
	Foreground(successColor).
	Bold(true)

// errorStyle creates style for error messages
var errorStyle = lipgloss.NewStyle().
	Foreground(errorColor).
	Bold(true)

// headerStyle creates the header/banner style
var headerStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(fgColor).
	Background(primaryColor).
	Padding(0, 2).
	MarginBottom(1)

// inputLabelStyle creates the style for input labels
var inputLabelStyle = lipgloss.NewStyle().
	Foreground(secondaryColor).
	Bold(true)

// progressStyle creates the style for progress indicators
var progressStyle = lipgloss.NewStyle().
	Foreground(accentColor)

// statusBarStyle creates the style for the status bar
var statusBarStyle = lipgloss.NewStyle().
	Foreground(mutedColor).
	Background(highlightBg).
	Padding(0, 1)

// GetTitleStyle returns the title style
func GetTitleStyle() lipgloss.Style {
	return titleStyle
}

// GetSubtitleStyle returns the subtitle style
func GetSubtitleStyle() lipgloss.Style {
	return subtitleStyle
}

// GetMenuItemStyle returns the menu item style
func GetMenuItemStyle() lipgloss.Style {
	return menuItemStyle
}

// GetSelectedMenuItemStyle returns the selected menu item style
func GetSelectedMenuItemStyle() lipgloss.Style {
	return selectedMenuItemStyle
}

// GetCursorStyle returns the cursor style
func GetCursorStyle() lipgloss.Style {
	return cursorStyle
}

// GetHelpStyle returns the help style
func GetHelpStyle() lipgloss.Style {
	return helpStyle
}

// GetBoxStyle returns the box style
func GetBoxStyle() lipgloss.Style {
	return boxStyle
}

// GetSuccessStyle returns the success style
func GetSuccessStyle() lipgloss.Style {
	return successStyle
}

// GetErrorStyle returns the error style
func GetErrorStyle() lipgloss.Style {
	return errorStyle
}

// GetHeaderStyle returns the header style
func GetHeaderStyle() lipgloss.Style {
	return headerStyle
}

// GetInputLabelStyle returns the input label style
func GetInputLabelStyle() lipgloss.Style {
	return inputLabelStyle
}

// GetProgressStyle returns the progress style
func GetProgressStyle() lipgloss.Style {
	return progressStyle
}

// GetStatusBarStyle returns the status bar style
func GetStatusBarStyle() lipgloss.Style {
	return statusBarStyle
}
