package core

import (
	"fmt"
	"log"
	"os"
)

// Logger wraps the standard log.Logger for custom logging.
type Logger struct {
    *log.Logger
}

// NewLogger creates a new Logger that writes to the specified file or stdout.
func NewLogger(logFile string) *Logger {
    var output *os.File
    var err error
    if logFile != "" {
        output, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
        if err != nil {
            fmt.Printf("Failed to open log file %s: %v. Logging to stdout.\n", logFile, err)
            output = os.Stdout
        }
    } else {
        output = os.Stdout
    }
    return &Logger{log.New(output, "", log.LstdFlags)}
}

// Info logs an informational message.
func (l *Logger) Info(msg string) {
    l.Println("[INFO]", msg)
}

// Error logs an error message.
func (l *Logger) Error(msg string) {
    l.Println("[ERROR]", msg)
}
