package output

import (
	"fmt"
	"os"
)

// Colors for terminal output
const (
	Reset  = "\033[0m"
	Red    = "\033[31m"
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Blue   = "\033[34m"
	Cyan   = "\033[36m"
	Gray   = "\033[90m"
	Bold   = "\033[1m"
)

// Output handles formatted console output.
type Output struct {
	debug bool
}

// New creates a new Output instance.
func New(debug bool) *Output {
	return &Output{debug: debug}
}

// Info prints an informational message.
func (o *Output) Info(format string, args ...any) {
	fmt.Printf(Cyan+"ℹ "+Reset+format+"\n", args...)
}

// Success prints a success message.
func (o *Output) Success(format string, args ...any) {
	fmt.Printf(Green+"✓ "+Reset+format+"\n", args...)
}

// Warning prints a warning message.
func (o *Output) Warning(format string, args ...any) {
	fmt.Printf(Yellow+"⚠ "+Reset+format+"\n", args...)
}

// Error prints an error message.
func (o *Output) Error(format string, args ...any) {
	fmt.Fprintf(os.Stderr, Red+"✗ "+Reset+format+"\n", args...)
}

// Debug prints a debug message if debug mode is enabled.
func (o *Output) Debug(format string, args ...any) {
	if o.debug {
		fmt.Printf(Gray+"[DEBUG] "+format+Reset+"\n", args...)
	}
}

// Print prints a plain message.
func (o *Output) Print(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

// Header prints a section header.
func (o *Output) Header(title string) {
	fmt.Printf("\n"+Bold+"%s"+Reset+"\n", title)
	fmt.Println(Gray + "─────────────────────────────────────────" + Reset)
}
