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

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`

func TestMCPServesOverPipesAndExitsCleanly(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	var stderr bytes.Buffer

	exit := make(chan int, 1)
	go func() {
		code := cli.Main(t.Context(),
			[]string{"--repo", repo(t), "--color", "never", "mcp"},
			cli.IO{In: stdinR, Out: stdoutW, Err: &stderr})

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

	_ = stdinW.Close()

	select {
	case code := <-exit:
		if code != cli.ExitOK {
			t.Errorf("exit = %d, want %d, a client disconnect is not a failure\nstderr: %s",
				code, cli.ExitOK, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("abel mcp did not exit after the client disconnected")
	}

	if !strings.Contains(stderr.String(), "MCP server ready") {
		t.Errorf("the readiness banner did not go to stderr: %q", stderr.String())
	}
}
