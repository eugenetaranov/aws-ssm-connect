package selector

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/gdamore/tcell/v2"
)

// Instance represents an EC2 instance for selection.
type Instance struct {
	ID        string
	Name      string
	PrivateIP string
}

// SelectInstance presents an interactive fuzzy finder for instance selection.
// Supports multi-word AND filtering (space-separated words all must match).
// If recentIDs is provided, those instances appear at the top of the list.
func SelectInstance(instances []Instance, recentIDs ...string) (Instance, error) {
	if len(instances) == 0 {
		return Instance{}, fmt.Errorf("no instances available")
	}

	// Build set of recent IDs for highlighting
	recentSet := make(map[string]bool)
	for _, id := range recentIDs {
		recentSet[id] = true
	}

	// Sort recent instances to the top
	if len(recentIDs) > 0 {
		instances = sortByRecent(instances, recentIDs)
	}

	// Save original file descriptors BEFORE tcell takes over.
	// tcell's Fini() closes stdin/stdout/stderr on macOS, so we need to
	// restore them afterward for subprocess execution to work properly.
	savedStdin, _ := syscall.Dup(int(os.Stdin.Fd()))
	savedStdout, _ := syscall.Dup(int(os.Stdout.Fd()))
	savedStderr, _ := syscall.Dup(int(os.Stderr.Fd()))

	screen, err := tcell.NewScreen()
	if err != nil {
		syscall.Close(savedStdin)
		syscall.Close(savedStdout)
		syscall.Close(savedStderr)
		return Instance{}, fmt.Errorf("failed to create screen: %w", err)
	}
	if err := screen.Init(); err != nil {
		syscall.Close(savedStdin)
		syscall.Close(savedStdout)
		syscall.Close(savedStderr)
		return Instance{}, fmt.Errorf("failed to init screen: %w", err)
	}

	// cleanupScreen must be called before any return to restore terminal state
	cleanupScreen := func() {
		screen.Fini()
		// Restore original file descriptors that tcell's Fini() closed
		_ = syscall.Dup2(savedStdin, int(os.Stdin.Fd()))
		_ = syscall.Dup2(savedStdout, int(os.Stdout.Fd()))
		_ = syscall.Dup2(savedStderr, int(os.Stderr.Fd()))
		_ = syscall.Close(savedStdin)
		_ = syscall.Close(savedStdout)
		_ = syscall.Close(savedStderr)
		// Reset terminal to sane state after restoring FDs
		_ = exec.Command("stty", "sane").Run()
	}

	query := ""
	cursor := 0
	selected := 0

	for {
		filtered := filterInstances(instances, query)
		if selected >= len(filtered) {
			selected = len(filtered) - 1
		}
		if selected < 0 {
			selected = 0
		}

		drawScreen(screen, filtered, len(instances), query, cursor, selected, recentSet)
		screen.Show()

		ev := screen.PollEvent()
		switch ev := ev.(type) {
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC:
				cleanupScreen()
				return Instance{}, fmt.Errorf("selection cancelled")
			case tcell.KeyEnter:
				if len(filtered) > 0 {
					cleanupScreen()
					return filtered[selected], nil
				}
			case tcell.KeyBackspace, tcell.KeyBackspace2:
				if cursor > 0 {
					query = query[:cursor-1] + query[cursor:]
					cursor--
				}
			case tcell.KeyDelete:
				if cursor < len(query) {
					query = query[:cursor] + query[cursor+1:]
				}
			case tcell.KeyLeft:
				if cursor > 0 {
					cursor--
				}
			case tcell.KeyRight:
				if cursor < len(query) {
					cursor++
				}
			case tcell.KeyUp, tcell.KeyCtrlP:
				if selected > 0 {
					selected--
				}
			case tcell.KeyDown, tcell.KeyCtrlN:
				if selected < len(filtered)-1 {
					selected++
				}
			case tcell.KeyCtrlU:
				query = query[cursor:]
				cursor = 0
			case tcell.KeyCtrlA:
				cursor = 0
			case tcell.KeyCtrlE:
				cursor = len(query)
			case tcell.KeyRune:
				query = query[:cursor] + string(ev.Rune()) + query[cursor:]
				cursor++
				selected = 0 // Reset selection on new input
			}
		case *tcell.EventResize:
			screen.Sync()
		}
	}
}

func sortByRecent(instances []Instance, recentIDs []string) []Instance {
	// Build priority map: lower index = more recent = higher priority
	priority := make(map[string]int)
	for i, id := range recentIDs {
		priority[id] = i + 1 // 1-indexed so 0 means "not recent"
	}

	// Separate recent and non-recent
	var recent, other []Instance
	for _, inst := range instances {
		if priority[inst.ID] > 0 {
			recent = append(recent, inst)
		} else {
			other = append(other, inst)
		}
	}

	// Sort recent by priority (most recent first)
	for i := 0; i < len(recent)-1; i++ {
		for j := i + 1; j < len(recent); j++ {
			if priority[recent[i].ID] > priority[recent[j].ID] {
				recent[i], recent[j] = recent[j], recent[i]
			}
		}
	}

	return append(recent, other...)
}

func filterInstances(instances []Instance, query string) []Instance {
	if query == "" {
		return instances
	}

	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return instances
	}

	var filtered []Instance
	for _, inst := range instances {
		searchStr := strings.ToLower(fmt.Sprintf("%s %s %s", inst.ID, inst.Name, inst.PrivateIP))
		if matchesAllWords(searchStr, words) {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

func matchesAllWords(s string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(s, word) {
			return false
		}
	}
	return true
}

func drawScreen(screen tcell.Screen, filtered []Instance, total int, query string, cursor, selected int, recentSet map[string]bool) {
	screen.Clear()
	w, h := screen.Size()

	promptStyle := tcell.StyleDefault.Foreground(tcell.ColorGreen).Bold(true)
	inputStyle := tcell.StyleDefault
	normalStyle := tcell.StyleDefault
	recentStyle := tcell.StyleDefault.Foreground(tcell.ColorYellow)
	selectedStyle := tcell.StyleDefault.Background(tcell.ColorDarkCyan).Foreground(tcell.ColorWhite)
	dimStyle := tcell.StyleDefault.Foreground(tcell.ColorGray)
	countStyle := tcell.StyleDefault.Foreground(tcell.ColorYellow)

	// Draw prompt
	prompt := "> "
	drawString(screen, 0, 0, prompt, promptStyle)
	drawString(screen, len(prompt), 0, query, inputStyle)

	// Draw cursor
	screen.ShowCursor(len(prompt)+cursor, 0)

	// Draw count
	countStr := fmt.Sprintf("  %d/%d", len(filtered), total)
	drawString(screen, len(prompt)+len(query), 0, countStr, countStyle)

	// Draw separator
	drawString(screen, 0, 1, strings.Repeat("─", w), dimStyle)

	// Draw instances
	maxVisible := h - 3
	startIdx := 0
	if selected >= maxVisible {
		startIdx = selected - maxVisible + 1
	}

	for i := 0; i < maxVisible && startIdx+i < len(filtered); i++ {
		inst := filtered[startIdx+i]
		y := i + 2

		name := inst.Name
		if name == "" {
			name = "(no name)"
		}
		line := fmt.Sprintf("  %s  %-30s  %s", inst.ID, truncate(name, 30), inst.PrivateIP)

		style := normalStyle
		if recentSet[inst.ID] {
			style = recentStyle
		}
		if startIdx+i == selected {
			style = selectedStyle
			line = "> " + line[2:]
		}

		// Pad line to full width for selection highlight
		if len(line) < w {
			line += strings.Repeat(" ", w-len(line))
		}

		drawString(screen, 0, y, line, style)
	}

	// Draw help at bottom
	helpText := "↑/↓ navigate • Enter select • Esc cancel • Type to filter (words are AND-matched)"
	drawString(screen, 0, h-1, helpText, dimStyle)
}

func drawString(screen tcell.Screen, x, y int, s string, style tcell.Style) {
	for i, r := range s {
		screen.SetContent(x+i, y, r, nil, style)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// FindByName finds instances matching the given name filter.
// Returns all instances where all space-separated words match (case-insensitive).
func FindByName(instances []Instance, filter string) []Instance {
	return filterInstances(instances, filter)
}
