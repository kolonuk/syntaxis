// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// Files the user attaches to a message are uploaded into the workspace, and the message carries a
// list of them at its end. The list is plain text, so it survives in history, in exports and in the
// summaries of long conversations, and the model and the chat app read it the same way.
//
//	<attachments>
//	/workspace/.attachments/shot.png | image | 120394
//	</attachments>

// AttachmentDir is the workspace folder uploads go to.
const AttachmentDir = sandbox.WorkspacePath + "/.attachments"

// Limits on pictures sent inline to a model.
const (
	maxInlineImages = 4
	maxInlineBytes  = 5 << 20
)

// Attachment is one attached file.
type Attachment struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	Size int64  `json:"size"`
}

const (
	attachOpen  = "<attachments>"
	attachClose = "</attachments>"
)

// ComposeAttachments adds the attachment list to the end of a message.
func ComposeAttachments(text string, items []Attachment) string {
	if len(items) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(text, "\n"))
	b.WriteString("\n\n" + attachOpen + "\n")
	for _, it := range items {
		// a name with a newline or "|" in it cannot be told from the list's own structure
		p := strings.NewReplacer("\n", " ", "\r", " ", "|", "/").Replace(it.Path)
		fmt.Fprintf(&b, "%s | %s | %d\n", p, it.Kind, it.Size)
	}
	b.WriteString(attachClose)
	return b.String()
}

// ParseAttachments splits a message into its text and its attachment list. Only a list at the very
// end of the message counts, and only entries inside the attachment folder are accepted: text
// elsewhere in a message (or in a file a tool printed) cannot name other files.
func ParseAttachments(content string) (string, []Attachment) {
	trimmed := strings.TrimRight(content, "\n ")
	if !strings.HasSuffix(trimmed, attachClose) {
		return content, nil
	}
	i := strings.LastIndex(trimmed, attachOpen)
	if i < 0 {
		return content, nil
	}
	body := trimmed[i+len(attachOpen) : len(trimmed)-len(attachClose)]
	var items []Attachment
	for _, line := range strings.Split(body, "\n") {
		f := strings.Split(strings.TrimSpace(line), " | ")
		if len(f) != 3 {
			continue
		}
		p := path.Clean(f[0])
		if !strings.HasPrefix(p, AttachmentDir+"/") {
			continue
		}
		k := Kind(f[1])
		if k != KindText && k != KindPDF && k != KindImage {
			continue
		}
		n, _ := strconv.ParseInt(f[2], 10, 64)
		items = append(items, Attachment{Path: p, Kind: k, Size: n})
	}
	return strings.TrimRight(trimmed[:i], "\n"), items
}

// LoadImages reads the pictures of a message so a vision model can see them. Unreadable or oversized
// pictures are skipped (the model can still read their text with read_file).
func LoadImages(ctx context.Context, exec func(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error), content string) []provider.Image {
	_, items := ParseAttachments(content)
	var out []provider.Image
	for _, it := range items {
		if it.Kind != KindImage || len(out) >= maxInlineImages || it.Size > maxInlineBytes {
			continue
		}
		mime := ImageMIME(it.Path)
		// browsers and models both handle these; bmp and tiff are left to OCR
		if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/webp" {
			continue
		}
		r, err := exec(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `[ -f "$1" ] && head -c "$2" -- "$1" | base64 -w0`, "_", it.Path, strconv.Itoa(maxInlineBytes)}, Timeout: 30 * time.Second, MaxOutput: maxInlineBytes*4/3 + 4096})
		if err != nil || r.ExitCode != 0 {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.Stdout))
		if err != nil || len(data) == 0 {
			continue
		}
		out = append(out, provider.Image{MIME: mime, Data: data})
	}
	return out
}
