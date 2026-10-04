//go:build !windows

package work

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/lib-agent-harness/session"
)

// Exercise the executable bridge input with the harness's one-MiB receiving frame
// limit. The attachment handler alone cannot protect that transport.
func TestAttachmentBridgeRefusesLargeInlineThenKeepsValidAttachment(t *testing.T) {
	runner := &scriptedRunner{writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	handler := designerTools(t, a, held, t.TempDir())
	var input strings.Builder
	for i, content := range []string{strings.Repeat("x", 2076340), "valid attachment"} {
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": "attach_file", "arguments": attachArgs("asset.txt", content, "")}})
		input.Write(request)
		input.WriteByte('\n')
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer writer.Close()
		// Use the executable bridge's pre-forwarding policy and the harness's
		// receiving frame limit, without binding a sandbox-forbidden Unix socket.
		scanner := bufio.NewScanner(roles.AttachmentBridgeInput(strings.NewReader(input.String())))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var request struct {
				ID     int              `json:"id"`
				Params session.ToolCall `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				done <- err
				return
			}
			result, err := handler.Handler().CallTool(context.Background(), request.Params)
			if err != nil {
				done <- err
				return
			}
			if err := json.NewEncoder(writer).Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
				done <- err
				return
			}
		}
		done <- scanner.Err()
	}()
	decoder := json.NewDecoder(reader)
	for i := 1; i <= 2; i++ {
		var response struct {
			ID     int                `json:"id"`
			Result session.ToolResult `json:"result"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.ID != i || response.Result.IsError != (i == 1) {
			t.Fatalf("response: %+v", response)
		}
		if i == 1 && !strings.Contains(response.Result.Content, "64 KiB") {
			t.Fatal(response.Result)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := taskByID(t, a, task.ID); len(got.Attachments) != 1 {
		t.Fatal(got.Attachments)
	}
}
