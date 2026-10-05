// Package outbox hands messages to Omnimo, the only program that talks to
// Telegram. Kronos writes one JSON file per message into Omnimo's outbox
// folder; Omnimo polls the folder, sends the message and deletes the file.
//
// The contract (folder, file naming, JSON v1) is defined in Omnimo's
// ARCHITECTURE.md, "Outbox contract". This is a producer-side copy of
// Omnimo's outbox.Write.
package outbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kinds Omnimo understands.
const (
	Notice   = "notice"   // sent as-is
	Alert    = "alert"    // sent as "⚠️ <source>: <title>\n<text>"
	Briefing = "briefing" // agenda sent as-is, then Nimo's commentary
)

// Message is version 1 of the outbox format.
type Message struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Title   string `json:"title,omitempty"`
	Text    string `json:"text"`
	Created string `json:"created"`
}

// Write queues one message: <dir>/<unix-ns>-<kind>.json, written as .tmp
// then renamed, so Omnimo never reads a half-written file.
func Write(dir, source, kind, title, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("outbox: refusing to queue an empty message")
	}
	switch kind {
	case Notice, Alert, Briefing:
	default:
		return "", fmt.Errorf("outbox: unknown kind %q (notice | alert | briefing)", kind)
	}

	now := time.Now()
	body, err := json.Marshal(Message{
		Version: 1,
		Kind:    kind,
		Source:  source,
		Title:   title,
		Text:    text,
		Created: now.Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("outbox: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("outbox: cannot create %s: %w", dir, err)
	}
	name := fmt.Sprintf("%d-%s.json", now.UnixNano(), kind)
	final := filepath.Join(dir, name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", fmt.Errorf("outbox: cannot write to %s: %w", dir, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("outbox: cannot write to %s: %w", dir, err)
	}
	return final, nil
}
