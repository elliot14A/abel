//go:build integration

package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/elliot14A/abel/internal/cli"
)

// initialize is the first message any MCP client sends.
const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`

// TestMCPServesOverPipesAndExitsCleanly drives `abel mcp` end to end over real
// pipes, the way an agent does.
//
// It exists for two regressions caught here: `abel mcp` reported a client
// disconnect — how every session ends — as an internal error and exited 70;
// and it used the SDK's StdioTransport, which reads the process's own streams
// and ignored the composition root's.
//
// It needs a Docker daemon because `abel mcp` builds its container client up
// front, on purpose: an agent should not discover mid-session that Docker is
// down.
func TestMCPServesOverPipesAndExitsCleanly(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	var stderr bytes.Buffer

	exit := make(chan int, 1)
	go func() {
		code := cli.Main(t.Context(),
			[]string{"--repo", repo(t), "--color", "never", "mcp"},
			cli.IO{In: stdinR, Out: stdoutW, Err: &stderr})
		// Closing the write end unblocks the reader below if the session ends
		// before it gets a response.
		_ = stdoutW.Close()
		exit <- code
	}()

	if _, err := io.WriteString(stdinW, initialize+"\n"); err != nil {
		t.Fatalf("write initialize: %v", err)
	}

	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutR)
		if scanner.Scan() {
			line <- scanner.Text()
		}
		close(line)
	}()

	var first string
	select {
	case got, ok := <-line:
		if !ok {
			t.Fatalf("the server closed stdout without responding\nstderr: %s", stderr.String())
		}
		first = got
	case <-time.After(30 * time.Second):
		t.Fatalf("no response within 30s\nstderr: %s", stderr.String())
	}

	// stdout must carry the JSON-RPC stream and nothing else: one stray log
	// line there ends the session for a real agent.
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(first), &response); err != nil {
		t.Fatalf("stdout is not JSON-RPC (%v): %q", err, first)
	}
	if response.JSONRPC != "2.0" || response.ID != 1 || len(response.Result) == 0 {
		t.Errorf("unexpected initialize response: %s", first)
	}

	// Now hang up, as an agent does when it is finished.
	_ = stdinW.Close()

	select {
	case code := <-exit:
		if code != cli.ExitOK {
			t.Errorf("exit = %d, want %d — a client disconnect is not a failure\nstderr: %s",
				code, cli.ExitOK, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("abel mcp did not exit after the client disconnected")
	}

	if !strings.Contains(stderr.String(), "MCP server ready") {
		t.Errorf("the readiness banner did not go to stderr: %q", stderr.String())
	}
}
