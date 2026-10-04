package roles

import (
	"context"
	"encoding/json"
	"io"

	"github.com/shhac/lib-agent-harness/session"
)

// Inline attachments leave ample room for JSON escaping in the harness's
// one-MiB protocol frames. Larger files travel by path or generated name.
const MaxInlineAttachmentBytes = 64 << 10
const InlineAttachmentGuide = "Inline content is limited to 64 KiB (65,536 bytes). Never paste larger files into a tool call; use path for an existing file."
const OversizedInlineAttachment = "crew: oversized inline attachment refused before forwarding"

// RunToolBridge guards attachment payloads before the harness frame scanner.
// Decode one request at a time so a refused call cannot close the channel.
func RunToolBridge(ctx context.Context, in io.Reader, out io.Writer) error {
	return session.RunBridge(ctx, AttachmentBridgeInput(in), out)
}

// AttachmentBridgeInput is the pre-forwarding input policy shared by the
// executable bridge and synthetic protocol fixtures. It needs no socket or
// live session to exercise oversized requests and subsequent calls.
func AttachmentBridgeInput(in io.Reader) io.Reader {
	return &attachmentRequests{decoder: json.NewDecoder(in)}
}

type attachmentRequests struct {
	decoder *json.Decoder
	pending []byte
}

func (r *attachmentRequests) Read(dst []byte) (int, error) {
	if len(r.pending) == 0 {
		var request map[string]json.RawMessage
		if err := r.decoder.Decode(&request); err != nil {
			return 0, err
		}
		var method string
		_ = json.Unmarshal(request["method"], &method)
		if method == "tools/call" {
			var params struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(request["params"], &params) == nil && params.Name == "attach_file" {
				var content string
				if json.Unmarshal(params.Arguments["content"], &content) == nil && len(content) > MaxInlineAttachmentBytes {
					params.Arguments["content"], _ = json.Marshal(OversizedInlineAttachment)
					request["params"], _ = json.Marshal(params)
				}
			}
		}
		data, err := json.Marshal(request)
		if err != nil {
			return 0, err
		}
		r.pending = append(data, '\n')
	}
	n := copy(dst, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
