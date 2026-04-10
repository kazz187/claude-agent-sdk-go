package claudeagent

// Portable session mutation functions.
//
// Rename/tag append typed metadata entries to the session's JSONL (matching
// the CLI pattern); delete removes the JSONL file; fork creates a new session
// with UUID remapping.
//
// Ported from claude-agent-sdk-python/_internal/session_mutations.py.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// RenameSession renames a session by appending a custom-title entry.
// Repeated calls are safe — the most recent wins.
func RenameSession(sessionID, title, directory string) error {
	if validateUUID(sessionID) == "" {
		return fmt.Errorf("invalid session_id: %s", sessionID)
	}
	stripped := strings.TrimSpace(title)
	if stripped == "" {
		return errors.New("title must be non-empty")
	}

	data, err := json.Marshal(map[string]any{
		"type":        "custom-title",
		"customTitle": stripped,
		"sessionId":   sessionID,
	})
	if err != nil {
		return err
	}
	return appendToSession(sessionID, string(data)+"\n", directory)
}

// TagSession tags a session. Pass an empty string to clear the tag.
// Tags are Unicode-sanitized before storing.
func TagSession(sessionID, tag, directory string) error {
	if validateUUID(sessionID) == "" {
		return fmt.Errorf("invalid session_id: %s", sessionID)
	}

	finalTag := ""
	clearing := tag == ""
	if !clearing {
		sanitized := strings.TrimSpace(sanitizeUnicode(tag))
		if sanitized == "" {
			return errors.New("tag must be non-empty (use empty string to clear)")
		}
		finalTag = sanitized
	}

	data, err := json.Marshal(map[string]any{
		"type":      "tag",
		"tag":       finalTag,
		"sessionId": sessionID,
	})
	if err != nil {
		return err
	}
	return appendToSession(sessionID, string(data)+"\n", directory)
}

// DeleteSession deletes a session by removing its JSONL file.
func DeleteSession(sessionID, directory string) error {
	if validateUUID(sessionID) == "" {
		return fmt.Errorf("invalid session_id: %s", sessionID)
	}
	path := findSessionFile(sessionID, directory)
	if path == "" {
		return fmt.Errorf("session %s not found", sessionID)
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("session %s not found", sessionID)
		}
		return err
	}
	return nil
}

// ForkSession forks a session into a new branch with fresh UUIDs.
// If upToMessageID is non-empty, the transcript is sliced up to (and
// including) that message. If title is empty, one is derived from the source.
func ForkSession(sessionID, directory, upToMessageID, title string) (*ForkSessionResult, error) {
	if validateUUID(sessionID) == "" {
		return nil, fmt.Errorf("invalid session_id: %s", sessionID)
	}
	if upToMessageID != "" && validateUUID(upToMessageID) == "" {
		return nil, fmt.Errorf("invalid up_to_message_id: %s", upToMessageID)
	}

	filePath, projectDir := findSessionFileWithDir(sessionID, directory)
	if filePath == "" {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("session %s has no messages to fork", sessionID)
	}

	transcript, contentReplacements := parseForkTranscript(content, sessionID)

	// Filter out sidechains
	var filtered []map[string]any
	for _, e := range transcript {
		if b, _ := e["isSidechain"].(bool); !b {
			filtered = append(filtered, e)
		}
	}
	transcript = filtered

	if len(transcript) == 0 {
		return nil, fmt.Errorf("session %s has no messages to fork", sessionID)
	}

	if upToMessageID != "" {
		cutoff := -1
		for i, e := range transcript {
			if uid, _ := e["uuid"].(string); uid == upToMessageID {
				cutoff = i
				break
			}
		}
		if cutoff == -1 {
			return nil, fmt.Errorf("message %s not found in session %s", upToMessageID, sessionID)
		}
		transcript = transcript[:cutoff+1]
	}

	uuidMapping := make(map[string]string, len(transcript))
	for _, e := range transcript {
		uid, _ := e["uuid"].(string)
		uuidMapping[uid] = newUUID()
	}

	// Filter out progress messages from written output
	var writable []map[string]any
	for _, e := range transcript {
		if t, _ := e["type"].(string); t != "progress" {
			writable = append(writable, e)
		}
	}
	if len(writable) == 0 {
		return nil, fmt.Errorf("session %s has no messages to fork", sessionID)
	}

	byUUID := make(map[string]map[string]any, len(transcript))
	for _, e := range transcript {
		uid, _ := e["uuid"].(string)
		byUUID[uid] = e
	}

	forkedSessionID := newUUID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var lines []string
	for i, original := range writable {
		originalUID, _ := original["uuid"].(string)
		newUUIDStr := uuidMapping[originalUID]

		// Resolve parentUuid, skipping progress ancestors
		var newParentUUID any
		parentID, _ := original["parentUuid"].(string)
		for parentID != "" {
			parent, ok := byUUID[parentID]
			if !ok {
				break
			}
			if pt, _ := parent["type"].(string); pt != "progress" {
				newParentUUID = uuidMapping[parentID]
				break
			}
			parentID, _ = parent["parentUuid"].(string)
		}

		timestamp := now
		if i != len(writable)-1 {
			if ts, ok := original["timestamp"].(string); ok && ts != "" {
				timestamp = ts
			}
		}

		// Remap logicalParentUuid
		var newLogicalParent any
		if lp, ok := original["logicalParentUuid"].(string); ok && lp != "" {
			newLogicalParent = uuidMapping[lp]
		}

		forked := make(map[string]any, len(original)+4)
		for k, v := range original {
			forked[k] = v
		}
		forked["uuid"] = newUUIDStr
		forked["parentUuid"] = newParentUUID
		forked["logicalParentUuid"] = newLogicalParent
		forked["sessionId"] = forkedSessionID
		forked["timestamp"] = timestamp
		forked["isSidechain"] = false
		forked["forkedFrom"] = map[string]any{
			"sessionId":   sessionID,
			"messageUuid": originalUID,
		}
		for _, k := range []string{"teamName", "agentName", "slug", "sourceToolAssistantUUID"} {
			delete(forked, k)
		}

		encoded, err := json.Marshal(forked)
		if err != nil {
			return nil, err
		}
		lines = append(lines, string(encoded))
	}

	if len(contentReplacements) > 0 {
		encoded, err := json.Marshal(map[string]any{
			"type":         "content-replacement",
			"sessionId":    forkedSessionID,
			"replacements": contentReplacements,
		})
		if err != nil {
			return nil, err
		}
		lines = append(lines, string(encoded))
	}

	// Derive title
	forkTitle := strings.TrimSpace(title)
	if forkTitle == "" {
		bufLen := len(content)
		headEnd := bufLen
		if headEnd > liteReadBufSize {
			headEnd = liteReadBufSize
		}
		tailStart := bufLen - liteReadBufSize
		if tailStart < 0 {
			tailStart = 0
		}
		head := string(content[:headEnd])
		tail := string(content[tailStart:])

		base := extractLastJSONStringField(tail, "customTitle")
		if base == "" {
			base = extractLastJSONStringField(head, "customTitle")
		}
		if base == "" {
			base = extractLastJSONStringField(tail, "aiTitle")
		}
		if base == "" {
			base = extractLastJSONStringField(head, "aiTitle")
		}
		if base == "" {
			base = extractFirstPromptFromHead(head)
		}
		if base == "" {
			base = "Forked session"
		}
		forkTitle = base + " (fork)"
	}

	encoded, err := json.Marshal(map[string]any{
		"type":        "custom-title",
		"sessionId":   forkedSessionID,
		"customTitle": forkTitle,
	})
	if err != nil {
		return nil, err
	}
	lines = append(lines, string(encoded))

	forkPath := filepath.Join(projectDir, forkedSessionID+".jsonl")
	f, err := os.OpenFile(forkPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		return nil, err
	}

	return &ForkSessionResult{SessionID: forkedSessionID}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newUUID generates a random UUIDv4 string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func findSessionFile(sessionID, directory string) string {
	p, _ := findSessionFileWithDir(sessionID, directory)
	return p
}

func findSessionFileWithDir(sessionID, directory string) (string, string) {
	fileName := sessionID + ".jsonl"

	tryDir := func(projectDir string) (string, string) {
		path := filepath.Join(projectDir, fileName)
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return path, projectDir
		}
		return "", ""
	}

	if directory != "" {
		canonical := canonicalizePath(directory)
		if projectDir := findProjectDir(canonical); projectDir != "" {
			if p, d := tryDir(projectDir); p != "" {
				return p, d
			}
		}
		for _, wt := range getWorktreePaths(canonical) {
			if wt == canonical {
				continue
			}
			if wtProjectDir := findProjectDir(wt); wtProjectDir != "" {
				if p, d := tryDir(wtProjectDir); p != "" {
					return p, d
				}
			}
		}
		return "", ""
	}

	projectsDir := getProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p, d := tryDir(filepath.Join(projectsDir, e.Name())); p != "" {
			return p, d
		}
	}
	return "", ""
}

var forkTranscriptTypes = map[string]bool{
	"user": true, "assistant": true, "attachment": true, "system": true, "progress": true,
}

// parseForkTranscript parses JSONL content into transcript entries +
// content-replacement records.
func parseForkTranscript(content []byte, sessionID string) ([]map[string]any, []any) {
	var transcript []map[string]any
	var contentReplacements []any

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entryType, _ := entry["type"].(string)
		if forkTranscriptTypes[entryType] {
			if _, ok := entry["uuid"].(string); ok {
				transcript = append(transcript, entry)
			}
		} else if entryType == "content-replacement" {
			if sid, _ := entry["sessionId"].(string); sid == sessionID {
				if reps, ok := entry["replacements"].([]any); ok {
					contentReplacements = append(contentReplacements, reps...)
				}
			}
		}
	}
	return transcript, contentReplacements
}

// appendToSession appends data to an existing session file.
func appendToSession(sessionID, data, directory string) error {
	fileName := sessionID + ".jsonl"

	if directory != "" {
		canonical := canonicalizePath(directory)
		if projectDir := findProjectDir(canonical); projectDir != "" {
			if ok, err := tryAppend(filepath.Join(projectDir, fileName), data); ok {
				return nil
			} else if err != nil {
				return err
			}
		}
		for _, wt := range getWorktreePaths(canonical) {
			if wt == canonical {
				continue
			}
			if wtProjectDir := findProjectDir(wt); wtProjectDir != "" {
				if ok, err := tryAppend(filepath.Join(wtProjectDir, fileName), data); ok {
					return nil
				} else if err != nil {
					return err
				}
			}
		}
		return fmt.Errorf("session %s not found in project directory for %s", sessionID, directory)
	}

	projectsDir := getProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return fmt.Errorf("session %s not found (no projects directory): %w", sessionID, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ok, err := tryAppend(filepath.Join(projectsDir, e.Name(), fileName), data); ok {
			return nil
		} else if err != nil {
			return err
		}
	}
	return fmt.Errorf("session %s not found in any project directory", sessionID)
}

// tryAppend tries to append to a path. Returns (ok, err):
//   - (true, nil) on successful write
//   - (false, nil) if the file does not exist or is 0-byte
//   - (false, err) on other I/O errors
func tryAppend(path, data string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return false, err
	}
	if stat.Size() == 0 {
		return false, nil
	}
	if _, err := f.WriteString(data); err != nil {
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Unicode sanitization
// ---------------------------------------------------------------------------

// sanitizeUnicode strips dangerous Unicode characters (zero-width spaces,
// directional marks, private-use, unassigned) and applies NFKC normalization.
// Iterates until stable or 10 iterations.
func sanitizeUnicode(value string) string {
	current := value
	for range 10 {
		previous := current

		// Strip Cf (format), Co (private use), Cn (unassigned) characters.
		var b strings.Builder
		b.Grow(len(current))
		for _, r := range current {
			if unicode.In(r, unicode.Cf, unicode.Co) {
				continue
			}
			if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
				continue
			}
			// Explicit dangerous ranges
			if (r >= 0x200b && r <= 0x200f) ||
				(r >= 0x202a && r <= 0x202e) ||
				(r >= 0x2066 && r <= 0x2069) ||
				r == 0xfeff ||
				(r >= 0xe000 && r <= 0xf8ff) {
				continue
			}
			b.WriteRune(r)
		}
		current = b.String()

		if current == previous {
			break
		}
	}
	return current
}
