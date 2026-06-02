// Package transcript parses Claude Code / Codex CLI transcripts and
// produces a uniform Stats record for sync-db.
package transcript

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Stats holds computed metrics from one transcript file. The same record
// shape is used for both Claude Code and Codex CLI; per-agent fields that
// have no equivalent are 0 (e.g. AskUserQuestion is always 0 for Codex,
// ReasoningTokens is always 0 for Claude).
type Stats struct {
	ToolUseTotal     int
	MidSessionMsgs   int
	AskUserQuestion  int
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	ReasoningTokens  int64
	Model            string
	IsGhost          bool // true if no user message exists
	LastTimestamp    time.Time
}

// Parse dispatches to the agent-specific parser. agentName must be
// "claude" or "codex"; an unknown name is treated as Claude for
// backward compatibility with pre-Codex session-index entries.
func Parse(transcriptPath, agentName string) Stats {
	switch agentName {
	case "codex":
		return ParseCodex(transcriptPath)
	default:
		return ParseClaude(transcriptPath)
	}
}

// MaxDecodedBytes caps how many *decoded* bytes a single transcript parse
// reads, applied after zstd decompression. It bounds two local-DoS vectors
// from a hostile/runaway agent session (issue 0060): a zstd "zip bomb"
// (tiny .jsonl.zst that inflates to GBs) and a plainly huge .jsonl pinning
// CPU during the scan. 256 MB sits comfortably above any realistic
// single-user session transcript, so legitimate data is never truncated;
// past the cap the scanner just stops, yielding partial stats (graceful
// degrade) rather than OOM. It is a var so tests can shrink it.
var MaxDecodedBytes int64 = 256 * 1024 * 1024

// openTranscript opens a transcript file, transparently decompressing
// .jsonl.zst rollouts. The caller must close the returned ReadCloser.
//
// Codex archives older sessions as zstd; Claude never compresses, so the
// decompression branch only kicks in when needed. The returned reader is
// capped at MaxDecodedBytes in both branches.
func openTranscript(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".zst") {
		return limitReadCloser(f, MaxDecodedBytes), nil
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return limitReadCloser(&zstdCloser{dec: dec, file: f}, MaxDecodedBytes), nil
}

// limitReadCloser caps reads at n decoded bytes while preserving the
// underlying Close (io.LimitReader alone drops the Closer).
func limitReadCloser(rc io.ReadCloser, n int64) io.ReadCloser {
	return &limitedReadCloser{r: io.LimitReader(rc, n), c: rc}
}

type limitedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.c.Close() }

// zstdCloser couples the zstd.Decoder lifecycle to the underlying file so
// callers only need a single Close().
type zstdCloser struct {
	dec  *zstd.Decoder
	file *os.File
}

func (z *zstdCloser) Read(p []byte) (int, error) { return z.dec.Read(p) }
func (z *zstdCloser) Close() error {
	z.dec.Close()
	return z.file.Close()
}

// ParseTimestamp parses a transcript timestamp string in the Claude format.
// Codex uses the same RFC3339-with-Z form so this works for both.
func ParseTimestamp(s string) (time.Time, error) {
	s = strings.Replace(s, "Z", "+00:00", 1)
	return time.Parse("2006-01-02T15:04:05+00:00", s)
}
