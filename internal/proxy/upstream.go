package proxy

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// UpstreamManager manages connection to upstream MCP server
type UpstreamManager struct {
	client  *mcp.Client
	session *mcp.ClientSession
	logger  *logger.Logger
	command string
	args    []string
}

// NewUpstreamManager creates a new upstream connection manager
func NewUpstreamManager(logger *logger.Logger, command string, args []string) *UpstreamManager {
	impl := &mcp.Implementation{
		Name:    "feedbackloop-upstream-client",
		Version: "0.1.0",
	}

	client := mcp.NewClient(impl, nil)

	return &UpstreamManager{
		client:  client,
		logger:  logger,
		command: command,
		args:    args,
	}
}

// Connect establishes connection to upstream MCP server
func (um *UpstreamManager) Connect(ctx context.Context) error {
	correlationID := fmt.Sprintf("upstream-connect-%d", time.Now().UnixNano())

	um.logger.LogEvent("upstream_connecting", correlationID, map[string]interface{}{
		"command": um.command,
		"args":    um.args,
	})

	// Create command transport for upstream server
	cmd := exec.Command(um.command, um.args...)
	transport := &mcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: 5 * time.Second,
	}

	// Connect to upstream server with timeout
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	session, err := um.client.Connect(connectCtx, transport, nil)
	if err != nil {
		um.logger.LogErrorEvent("upstream_connect_failed", correlationID, map[string]interface{}{
			"error":   err.Error(),
			"command": um.command,
			"args":    um.args,
		})
		return fmt.Errorf("failed to connect to upstream server: %w", err)
	}

	um.session = session

	um.logger.LogEvent("upstream_connected", correlationID, map[string]interface{}{
		"session_id": session.ID(),
		"command":    um.command,
		"args":       um.args,
	})

	return nil
}

// Session returns the active client session
func (um *UpstreamManager) Session() *mcp.ClientSession {
	return um.session
}

// Wait waits for the upstream session to terminate
// Returns error if upstream crashes or exits unexpectedly
func (um *UpstreamManager) Wait() error {
	if um.session == nil {
		return fmt.Errorf("no active session")
	}

	correlationID := fmt.Sprintf("upstream-wait-%d", time.Now().UnixNano())

	um.logger.LogEvent("upstream_waiting", correlationID, map[string]interface{}{
		"session_id": um.session.ID(),
	})

	err := um.session.Wait()
	if err != nil {
		um.logger.LogErrorEvent("upstream_terminated_with_error", correlationID, map[string]interface{}{
			"error":      err.Error(),
			"session_id": um.session.ID(),
		})
		return fmt.Errorf("upstream session terminated: %w", err)
	}

	um.logger.LogEvent("upstream_terminated", correlationID, map[string]interface{}{
		"session_id": um.session.ID(),
	})

	return nil
}

// Close terminates the upstream connection
func (um *UpstreamManager) Close() error {
	if um.session == nil {
		return nil
	}

	correlationID := fmt.Sprintf("upstream-close-%d", time.Now().UnixNano())

	um.logger.LogEvent("upstream_closing", correlationID, map[string]interface{}{
		"session_id": um.session.ID(),
	})

	err := um.session.Close()
	if err != nil {
		um.logger.LogErrorEvent("upstream_close_failed", correlationID, map[string]interface{}{
			"error":      err.Error(),
			"session_id": um.session.ID(),
		})
		return fmt.Errorf("failed to close upstream session: %w", err)
	}

	um.logger.LogEvent("upstream_closed", correlationID, map[string]interface{}{
		"session_id": um.session.ID(),
	})

	return nil
}

// Ping sends a ping to the upstream server
func (um *UpstreamManager) Ping(ctx context.Context) error {
	if um.session == nil {
		return fmt.Errorf("no active session")
	}

	correlationID := fmt.Sprintf("upstream-ping-%d", time.Now().UnixNano())

	um.logger.LogOutbound("ping_request", correlationID, map[string]interface{}{
		"session_id": um.session.ID(),
	})

	err := um.session.Ping(ctx, &mcp.PingParams{})
	if err != nil {
		um.logger.LogErrorOutbound("ping_failed", correlationID, map[string]interface{}{
			"error":      err.Error(),
			"session_id": um.session.ID(),
		})
		return fmt.Errorf("upstream ping failed: %w", err)
	}

	um.logger.LogInbound("ping_response", correlationID, map[string]interface{}{
		"session_id": um.session.ID(),
	})

	return nil
}
