package claudeagent

import (
	"context"
)

// Transport defines the interface for communication with Claude.
//
// This is a low-level transport interface that handles raw I/O with the Claude
// process or service. The Query struct builds on top of this to implement the
// control protocol and message routing.
//
// WARNING: This internal API is exposed for custom transport implementations
// (e.g., remote Claude Code connections). The interface may change in future releases.
type Transport interface {
	// Connect establishes the transport connection and prepares for communication.
	// For subprocess transports, this starts the process.
	// For network transports, this establishes the connection.
	Connect(ctx context.Context) error

	// Write sends raw data to the transport.
	// The data is typically JSON followed by a newline.
	Write(ctx context.Context, data string) error

	// ReadMessages returns a channel that yields parsed JSON messages from the transport.
	// The channel is closed when the transport is closed or an error occurs.
	ReadMessages(ctx context.Context) (<-chan RawMessage, <-chan error)

	// Close closes the transport connection and cleans up resources.
	Close() error

	// IsReady returns true if the transport is ready to send/receive messages.
	IsReady() bool

	// EndInput ends the input stream (closes stdin for process transports).
	EndInput() error
}
