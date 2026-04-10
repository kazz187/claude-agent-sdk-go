package claudeagent

// Session listing and reading.
//
// Scans ~/.claude/projects/<sanitized-cwd>/ for .jsonl session files and
// extracts metadata from stat + head/tail reads without full JSONL parsing.
// Ported from claude-agent-sdk-python/_internal/sessions.py.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	liteReadBufSize     = 65536
	maxSanitizedLength  = 200
	firstPromptMaxChars = 200
)

var (
	uuidRE         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sanitizeRE     = regexp.MustCompile(`[^a-zA-Z0-9]`)
	commandNameRE  = regexp.MustCompile(`<command-name>(.*?)</command-name>`)
	skipPromptRE   = regexp.MustCompile(`^(?:<local-command-stdout>|<session-start-hook>|<tick>|<goal>|\[Request interrupted by user[^\]]*\])`)
	skipIDEOpenRE  = regexp.MustCompile(`(?s)^\s*<ide_opened_file>.*</ide_opened_file>\s*$`)
	skipIDESelRE   = regexp.MustCompile(`(?s)^\s*<ide_selection>.*</ide_selection>\s*$`)
)

// ---------------------------------------------------------------------------
// UUID / Path helpers
// ---------------------------------------------------------------------------

func validateUUID(maybe string) string {
	if uuidRE.MatchString(maybe) {
		return maybe
	}
	return ""
}

// simpleHash is a 32-bit integer hash to base36, matching the CLI's
// simpleHash() used for directory naming on long paths. Mirrors the JS:
//
//	h = (h << 5) - h + charCodeAt
//	h |= 0
//	Math.abs(h).toString(36)
func simpleHash(s string) string {
	var h int32
	for _, ch := range s {
		h = (h << 5) - h + int32(ch)
	}
	// JS Math.abs on an int32; int32 min negates to itself in Go (wraps),
	// so widen to int64 for the absolute value.
	abs := int64(h)
	if abs < 0 {
		abs = -abs
	}
	if abs == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var out []byte
	for n := abs; n > 0; n /= 36 {
		out = append([]byte{digits[n%36]}, out...)
	}
	return string(out)
}

// sanitizePath makes a string safe for use as a directory name.
// Replaces non-alphanumeric characters with hyphens; truncates + hash-suffix
// for paths exceeding maxSanitizedLength.
func sanitizePath(name string) string {
	sanitized := sanitizeRE.ReplaceAllString(name, "-")
	if len(sanitized) <= maxSanitizedLength {
		return sanitized
	}
	h := simpleHash(name)
	return sanitized[:maxSanitizedLength] + "-" + h
}

func getClaudeConfigHomeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func getProjectsDir() string {
	return filepath.Join(getClaudeConfigHomeDir(), "projects")
}

func getProjectDir(projectPath string) string {
	return filepath.Join(getProjectsDir(), sanitizePath(projectPath))
}

func canonicalizePath(d string) string {
	if resolved, err := filepath.EvalSymlinks(d); err == nil {
		return resolved
	}
	return d
}

// findProjectDir returns the project directory for a given path, tolerating
// hash mismatches for long paths via prefix-based scanning.
func findProjectDir(projectPath string) string {
	exact := getProjectDir(projectPath)
	if info, err := os.Stat(exact); err == nil && info.IsDir() {
		return exact
	}

	sanitized := sanitizePath(projectPath)
	if len(sanitized) <= maxSanitizedLength {
		return ""
	}
	prefix := sanitized[:maxSanitizedLength]
	projectsDir := getProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix+"-") {
			return filepath.Join(projectsDir, e.Name())
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// JSON string field extraction (no full parse, works on truncated lines)
// ---------------------------------------------------------------------------

func unescapeJSONString(raw string) string {
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var result string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &result); err != nil {
		return raw
	}
	return result
}

func extractJSONStringField(text, key string) string {
	for _, pattern := range []string{`"` + key + `":"`, `"` + key + `": "`} {
		idx := strings.Index(text, pattern)
		if idx < 0 {
			continue
		}
		valueStart := idx + len(pattern)
		i := valueStart
		for i < len(text) {
			if text[i] == '\\' {
				i += 2
				continue
			}
			if text[i] == '"' {
				return unescapeJSONString(text[valueStart:i])
			}
			i++
		}
	}
	return ""
}

func extractLastJSONStringField(text, key string) string {
	var last string
	for _, pattern := range []string{`"` + key + `":"`, `"` + key + `": "`} {
		searchFrom := 0
		for {
			idx := strings.Index(text[searchFrom:], pattern)
			if idx < 0 {
				break
			}
			idx += searchFrom
			valueStart := idx + len(pattern)
			i := valueStart
			for i < len(text) {
				if text[i] == '\\' {
					i += 2
					continue
				}
				if text[i] == '"' {
					last = unescapeJSONString(text[valueStart:i])
					break
				}
				i++
			}
			searchFrom = i + 1
			if searchFrom >= len(text) {
				break
			}
		}
	}
	return last
}

// ---------------------------------------------------------------------------
// First prompt extraction from head chunk
// ---------------------------------------------------------------------------

func extractFirstPromptFromHead(head string) string {
	start := 0
	commandFallback := ""

	for start < len(head) {
		var line string
		nlIdx := strings.Index(head[start:], "\n")
		if nlIdx >= 0 {
			line = head[start : start+nlIdx]
			start += nlIdx + 1
		} else {
			line = head[start:]
			start = len(head)
		}

		if !strings.Contains(line, `"type":"user"`) && !strings.Contains(line, `"type": "user"`) {
			continue
		}
		if strings.Contains(line, `"tool_result"`) {
			continue
		}
		if strings.Contains(line, `"isMeta":true`) || strings.Contains(line, `"isMeta": true`) {
			continue
		}
		if strings.Contains(line, `"isCompactSummary":true`) || strings.Contains(line, `"isCompactSummary": true`) {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if t, _ := entry["type"].(string); t != "user" {
			continue
		}

		message, _ := entry["message"].(map[string]any)
		if message == nil {
			continue
		}

		var texts []string
		switch content := message["content"].(type) {
		case string:
			texts = append(texts, content)
		case []any:
			for _, b := range content {
				block, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if bt, _ := block["type"].(string); bt == "text" {
					if txt, ok := block["text"].(string); ok {
						texts = append(texts, txt)
					}
				}
			}
		}

		for _, raw := range texts {
			result := strings.TrimSpace(strings.ReplaceAll(raw, "\n", " "))
			if result == "" {
				continue
			}

			if m := commandNameRE.FindStringSubmatch(result); m != nil {
				if commandFallback == "" {
					commandFallback = m[1]
				}
				continue
			}
			if skipPromptRE.MatchString(result) || skipIDEOpenRE.MatchString(result) || skipIDESelRE.MatchString(result) {
				continue
			}
			if len(result) > firstPromptMaxChars {
				result = strings.TrimRight(result[:firstPromptMaxChars], " \t") + "\u2026"
			}
			return result
		}
	}
	return commandFallback
}

// ---------------------------------------------------------------------------
// File head/tail reading
// ---------------------------------------------------------------------------

type liteSessionFile struct {
	mtime int64 // milliseconds
	size  int64
	head  string
	tail  string
}

func readSessionLite(path string) *liteSessionFile {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil
	}
	size := stat.Size()
	if size == 0 {
		return nil
	}
	mtime := stat.ModTime().UnixNano() / int64(time.Millisecond)

	headBuf := make([]byte, liteReadBufSize)
	n, _ := f.Read(headBuf)
	if n == 0 {
		return nil
	}
	head := string(headBuf[:n])

	var tail string
	tailOffset := size - int64(liteReadBufSize)
	if tailOffset <= 0 {
		tail = head
	} else {
		if _, err := f.Seek(tailOffset, 0); err != nil {
			tail = head
		} else {
			tailBuf := make([]byte, liteReadBufSize)
			n2, _ := f.Read(tailBuf)
			tail = string(tailBuf[:n2])
		}
	}

	return &liteSessionFile{mtime: mtime, size: size, head: head, tail: tail}
}

// ---------------------------------------------------------------------------
// Git worktree detection
// ---------------------------------------------------------------------------

func getWorktreePaths(cwd string) []string {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	return paths
}

// ---------------------------------------------------------------------------
// Field extraction
// ---------------------------------------------------------------------------

func parseSessionInfoFromLite(sessionID string, lite *liteSessionFile, projectPath string) *SDKSessionInfo {
	head, tail := lite.head, lite.tail

	firstNL := strings.Index(head, "\n")
	firstLine := head
	if firstNL >= 0 {
		firstLine = head[:firstNL]
	}
	if strings.Contains(firstLine, `"isSidechain":true`) || strings.Contains(firstLine, `"isSidechain": true`) {
		return nil
	}

	customTitle := extractLastJSONStringField(tail, "customTitle")
	if customTitle == "" {
		customTitle = extractLastJSONStringField(head, "customTitle")
	}
	if customTitle == "" {
		customTitle = extractLastJSONStringField(tail, "aiTitle")
	}
	if customTitle == "" {
		customTitle = extractLastJSONStringField(head, "aiTitle")
	}

	firstPrompt := extractFirstPromptFromHead(head)

	summary := customTitle
	if summary == "" {
		summary = extractLastJSONStringField(tail, "lastPrompt")
	}
	if summary == "" {
		summary = extractLastJSONStringField(tail, "summary")
	}
	if summary == "" {
		summary = firstPrompt
	}

	if summary == "" {
		return nil
	}

	gitBranch := extractLastJSONStringField(tail, "gitBranch")
	if gitBranch == "" {
		gitBranch = extractJSONStringField(head, "gitBranch")
	}

	sessionCwd := extractJSONStringField(head, "cwd")
	if sessionCwd == "" {
		sessionCwd = projectPath
	}

	// Tag: scope to {"type":"tag"} lines only (avoid tool_use "tag" fields).
	var tag string
	tailLines := strings.Split(tail, "\n")
	for i := len(tailLines) - 1; i >= 0; i-- {
		if strings.HasPrefix(tailLines[i], `{"type":"tag"`) {
			tag = extractLastJSONStringField(tailLines[i], "tag")
			break
		}
	}

	var createdAt *int64
	if ts := extractJSONStringField(firstLine, "timestamp"); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ms := t.UnixNano() / int64(time.Millisecond)
			createdAt = &ms
		}
	}

	size := lite.size
	info := &SDKSessionInfo{
		SessionID:    sessionID,
		Summary:      summary,
		LastModified: lite.mtime,
		FileSize:     &size,
		CustomTitle:  customTitle,
		FirstPrompt:  firstPrompt,
		GitBranch:    gitBranch,
		Cwd:          sessionCwd,
		Tag:          tag,
		CreatedAt:    createdAt,
	}
	return info
}

// ---------------------------------------------------------------------------
// Core listing implementation
// ---------------------------------------------------------------------------

func readSessionsFromDir(projectDir, projectPath string) []*SDKSessionInfo {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}
	var results []*SDKSessionInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sid := validateUUID(strings.TrimSuffix(name, ".jsonl"))
		if sid == "" {
			continue
		}
		lite := readSessionLite(filepath.Join(projectDir, name))
		if lite == nil {
			continue
		}
		if info := parseSessionInfoFromLite(sid, lite, projectPath); info != nil {
			results = append(results, info)
		}
	}
	return results
}

func deduplicateBySessionID(sessions []*SDKSessionInfo) []*SDKSessionInfo {
	byID := make(map[string]*SDKSessionInfo)
	for _, s := range sessions {
		existing, ok := byID[s.SessionID]
		if !ok || s.LastModified > existing.LastModified {
			byID[s.SessionID] = s
		}
	}
	result := make([]*SDKSessionInfo, 0, len(byID))
	for _, s := range byID {
		result = append(result, s)
	}
	return result
}

func applySortLimitOffset(sessions []*SDKSessionInfo, limit, offset int) []*SDKSessionInfo {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastModified > sessions[j].LastModified
	})
	if offset > 0 {
		if offset >= len(sessions) {
			return nil
		}
		sessions = sessions[offset:]
	}
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions
}

func listSessionsForProject(directory string, limit, offset int, includeWorktrees bool) []*SDKSessionInfo {
	canonical := canonicalizePath(directory)

	var worktreePaths []string
	if includeWorktrees {
		worktreePaths = getWorktreePaths(canonical)
	}

	if len(worktreePaths) <= 1 {
		projectDir := findProjectDir(canonical)
		if projectDir == "" {
			return nil
		}
		sessions := readSessionsFromDir(projectDir, canonical)
		return applySortLimitOffset(sessions, limit, offset)
	}

	projectsDir := getProjectsDir()
	caseInsensitive := runtime.GOOS == "windows"

	type indexed struct {
		wt     string
		prefix string
	}
	var idx []indexed
	for _, wt := range worktreePaths {
		s := sanitizePath(wt)
		p := s
		if caseInsensitive {
			p = strings.ToLower(p)
		}
		idx = append(idx, indexed{wt: wt, prefix: p})
	}
	sort.SliceStable(idx, func(i, j int) bool {
		return len(idx[i].prefix) > len(idx[j].prefix)
	})

	allDirents, err := os.ReadDir(projectsDir)
	if err != nil {
		projectDir := findProjectDir(canonical)
		if projectDir == "" {
			return applySortLimitOffset(nil, limit, offset)
		}
		sessions := readSessionsFromDir(projectDir, canonical)
		return applySortLimitOffset(sessions, limit, offset)
	}

	var allSessions []*SDKSessionInfo
	seenDirs := make(map[string]bool)

	if canonicalProjectDir := findProjectDir(canonical); canonicalProjectDir != "" {
		base := filepath.Base(canonicalProjectDir)
		key := base
		if caseInsensitive {
			key = strings.ToLower(key)
		}
		seenDirs[key] = true
		allSessions = append(allSessions, readSessionsFromDir(canonicalProjectDir, canonical)...)
	}

	for _, entry := range allDirents {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		key := dirName
		if caseInsensitive {
			key = strings.ToLower(key)
		}
		if seenDirs[key] {
			continue
		}
		for _, it := range idx {
			isMatch := key == it.prefix ||
				(len(it.prefix) >= maxSanitizedLength && strings.HasPrefix(key, it.prefix+"-"))
			if isMatch {
				seenDirs[key] = true
				allSessions = append(allSessions,
					readSessionsFromDir(filepath.Join(projectsDir, dirName), it.wt)...)
				break
			}
		}
	}

	return applySortLimitOffset(deduplicateBySessionID(allSessions), limit, offset)
}

func listAllSessions(limit, offset int) []*SDKSessionInfo {
	projectsDir := getProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var all []*SDKSessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		all = append(all, readSessionsFromDir(filepath.Join(projectsDir, e.Name()), "")...)
	}
	return applySortLimitOffset(deduplicateBySessionID(all), limit, offset)
}

// ListSessions lists sessions with metadata from stat + head/tail reads.
// When directory is empty, returns sessions across all projects.
// limit == 0 means unlimited. Sorted by LastModified descending.
func ListSessions(directory string, limit, offset int, includeWorktrees bool) []*SDKSessionInfo {
	if directory != "" {
		return listSessionsForProject(directory, limit, offset, includeWorktrees)
	}
	return listAllSessions(limit, offset)
}

// GetSessionInfo reads metadata for a single session by ID.
// Returns nil if the session file is not found, is a sidechain session,
// or has no extractable summary.
func GetSessionInfo(sessionID, directory string) *SDKSessionInfo {
	sid := validateUUID(sessionID)
	if sid == "" {
		return nil
	}
	fileName := sid + ".jsonl"

	if directory != "" {
		canonical := canonicalizePath(directory)
		if projectDir := findProjectDir(canonical); projectDir != "" {
			if lite := readSessionLite(filepath.Join(projectDir, fileName)); lite != nil {
				return parseSessionInfoFromLite(sid, lite, canonical)
			}
		}
		for _, wt := range getWorktreePaths(canonical) {
			if wt == canonical {
				continue
			}
			if wtProjectDir := findProjectDir(wt); wtProjectDir != "" {
				if lite := readSessionLite(filepath.Join(wtProjectDir, fileName)); lite != nil {
					return parseSessionInfoFromLite(sid, lite, wt)
				}
			}
		}
		return nil
	}

	projectsDir := getProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if lite := readSessionLite(filepath.Join(projectsDir, e.Name(), fileName)); lite != nil {
			return parseSessionInfoFromLite(sid, lite, "")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// get_session_messages — full transcript reconstruction
// ---------------------------------------------------------------------------

var transcriptEntryTypes = map[string]bool{
	"user": true, "assistant": true, "progress": true, "system": true, "attachment": true,
}

type transcriptEntry map[string]any

func tryReadSessionFile(projectDir, fileName string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, fileName))
	if err != nil {
		return ""
	}
	return string(data)
}

func readSessionFile(sessionID, directory string) string {
	fileName := sessionID + ".jsonl"

	if directory != "" {
		canonical := canonicalizePath(directory)
		if projectDir := findProjectDir(canonical); projectDir != "" {
			if content := tryReadSessionFile(projectDir, fileName); content != "" {
				return content
			}
		}
		for _, wt := range getWorktreePaths(canonical) {
			if wt == canonical {
				continue
			}
			if wtProjectDir := findProjectDir(wt); wtProjectDir != "" {
				if content := tryReadSessionFile(wtProjectDir, fileName); content != "" {
					return content
				}
			}
		}
		return ""
	}

	projectsDir := getProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if content := tryReadSessionFile(filepath.Join(projectsDir, e.Name()), fileName); content != "" {
			return content
		}
	}
	return ""
}

func parseTranscriptEntries(content string) []transcriptEntry {
	var entries []transcriptEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry transcriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entryType, _ := entry["type"].(string)
		if !transcriptEntryTypes[entryType] {
			continue
		}
		if _, ok := entry["uuid"].(string); !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func buildConversationChain(entries []transcriptEntry) []transcriptEntry {
	if len(entries) == 0 {
		return nil
	}

	byUUID := make(map[string]transcriptEntry, len(entries))
	entryIndex := make(map[string]int, len(entries))
	for i, e := range entries {
		uid, _ := e["uuid"].(string)
		byUUID[uid] = e
		entryIndex[uid] = i
	}

	parentUUIDs := make(map[string]bool)
	for _, e := range entries {
		if p, ok := e["parentUuid"].(string); ok && p != "" {
			parentUUIDs[p] = true
		}
	}

	var terminals []transcriptEntry
	for _, e := range entries {
		uid, _ := e["uuid"].(string)
		if !parentUUIDs[uid] {
			terminals = append(terminals, e)
		}
	}

	var leaves []transcriptEntry
	for _, terminal := range terminals {
		cur := terminal
		seen := make(map[string]bool)
		for cur != nil {
			uid, _ := cur["uuid"].(string)
			if seen[uid] {
				break
			}
			seen[uid] = true
			t, _ := cur["type"].(string)
			if t == "user" || t == "assistant" {
				leaves = append(leaves, cur)
				break
			}
			parent, _ := cur["parentUuid"].(string)
			if parent == "" {
				cur = nil
			} else {
				cur = byUUID[parent]
			}
		}
	}

	if len(leaves) == 0 {
		return nil
	}

	var mainLeaves []transcriptEntry
	for _, leaf := range leaves {
		if b, _ := leaf["isSidechain"].(bool); b {
			continue
		}
		if _, ok := leaf["teamName"]; ok {
			if t, _ := leaf["teamName"].(string); t != "" {
				continue
			}
		}
		if m, _ := leaf["isMeta"].(bool); m {
			continue
		}
		mainLeaves = append(mainLeaves, leaf)
	}

	pickBest := func(candidates []transcriptEntry) transcriptEntry {
		best := candidates[0]
		bid, _ := best["uuid"].(string)
		bestIdx := entryIndex[bid]
		for _, cur := range candidates[1:] {
			cid, _ := cur["uuid"].(string)
			if entryIndex[cid] > bestIdx {
				best = cur
				bestIdx = entryIndex[cid]
			}
		}
		return best
	}

	var leaf transcriptEntry
	if len(mainLeaves) > 0 {
		leaf = pickBest(mainLeaves)
	} else {
		leaf = pickBest(leaves)
	}

	var chain []transcriptEntry
	chainSeen := make(map[string]bool)
	cur := leaf
	for cur != nil {
		uid, _ := cur["uuid"].(string)
		if chainSeen[uid] {
			break
		}
		chainSeen[uid] = true
		chain = append(chain, cur)
		parent, _ := cur["parentUuid"].(string)
		if parent == "" {
			cur = nil
		} else {
			cur = byUUID[parent]
		}
	}

	// reverse
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func isVisibleMessage(entry transcriptEntry) bool {
	entryType, _ := entry["type"].(string)
	if entryType != "user" && entryType != "assistant" {
		return false
	}
	if b, _ := entry["isMeta"].(bool); b {
		return false
	}
	if b, _ := entry["isSidechain"].(bool); b {
		return false
	}
	if t, _ := entry["teamName"].(string); t != "" {
		return false
	}
	return true
}

func toSessionMessage(entry transcriptEntry) *SessionMessage {
	entryType, _ := entry["type"].(string)
	uid, _ := entry["uuid"].(string)
	sid, _ := entry["sessionId"].(string)
	msg, _ := entry["message"].(map[string]any)
	return &SessionMessage{
		Type:      entryType,
		UUID:      uid,
		SessionID: sid,
		Message:   msg,
	}
}

// GetSessionMessages reads a session's conversation messages from its JSONL
// transcript file. Returns an empty slice if the session is not found.
// limit == 0 means unlimited.
func GetSessionMessages(sessionID, directory string, limit, offset int) []*SessionMessage {
	if validateUUID(sessionID) == "" {
		return nil
	}
	content := readSessionFile(sessionID, directory)
	if content == "" {
		return nil
	}
	entries := parseTranscriptEntries(content)
	chain := buildConversationChain(entries)

	var messages []*SessionMessage
	for _, e := range chain {
		if isVisibleMessage(e) {
			messages = append(messages, toSessionMessage(e))
		}
	}

	if offset > 0 {
		if offset >= len(messages) {
			return nil
		}
		messages = messages[offset:]
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[:limit]
	}
	return messages
}

