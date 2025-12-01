package claude

import (
	"errors"
	"fmt"
)

// ClaudeSDKError is the base error type for all SDK errors.
type ClaudeSDKError struct {
	Message string
	Cause   error
}

func (e *ClaudeSDKError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *ClaudeSDKError) Unwrap() error {
	return e.Cause
}

// CLIConnectionError indicates a connection error to the Claude CLI.
type CLIConnectionError struct {
	ClaudeSDKError
}

// NewCLIConnectionError creates a new CLIConnectionError.
func NewCLIConnectionError(message string, cause error) *CLIConnectionError {
	return &CLIConnectionError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
	}
}

// CLINotFoundError indicates the Claude CLI was not found.
type CLINotFoundError struct {
	ClaudeSDKError
}

// NewCLINotFoundError creates a new CLINotFoundError.
func NewCLINotFoundError(message string) *CLINotFoundError {
	return &CLINotFoundError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
		},
	}
}

// ProcessError indicates a process execution error.
type ProcessError struct {
	ClaudeSDKError
	ExitCode int
	Stderr   string
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("%s (exit code: %d)", e.Message, e.ExitCode)
}

// NewProcessError creates a new ProcessError.
func NewProcessError(message string, exitCode int, stderr string) *ProcessError {
	return &ProcessError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
		},
		ExitCode: exitCode,
		Stderr:   stderr,
	}
}

// JSONDecodeError indicates a JSON decoding error.
type JSONDecodeError struct {
	ClaudeSDKError
	RawData string
}

// NewJSONDecodeError creates a new JSONDecodeError.
func NewJSONDecodeError(message string, cause error, rawData string) *JSONDecodeError {
	return &JSONDecodeError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
		RawData: rawData,
	}
}

// MessageParseError indicates a message parsing error.
type MessageParseError struct {
	ClaudeSDKError
	RawData any
}

// NewMessageParseError creates a new MessageParseError.
func NewMessageParseError(message string, rawData any) *MessageParseError {
	return &MessageParseError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
		},
		RawData: rawData,
	}
}

// Common sentinel errors
var (
	ErrNotConnected     = errors.New("not connected")
	ErrAlreadyConnected = errors.New("already connected")
	ErrTransportClosed  = errors.New("transport closed")
)
