// Package meter reads local usage telemetry. It never opens credentials or sends network requests.
package meter

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type Tokens struct {
	Input     int64 `json:"input"` // Includes cached input, excludes Claude cache creation only until normalized.
	Cached    int64 `json:"cached"`
	Write5m   int64 `json:"write_5m"`
	Write1h   int64 `json:"write_1h"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"` // A subset of Output, never added again.
}

func (t Tokens) Total() int64 { return t.Input + t.Output }
func (t Tokens) Add(u Tokens) Tokens {
	return Tokens{t.Input + u.Input, t.Cached + u.Cached, t.Write5m + u.Write5m, t.Write1h + u.Write1h, t.Output + u.Output, t.Reasoning + u.Reasoning}
}
func (t Tokens) Sub(u Tokens) Tokens {
	return Tokens{t.Input - u.Input, t.Cached - u.Cached, t.Write5m - u.Write5m, t.Write1h - u.Write1h, t.Output - u.Output, t.Reasoning - u.Reasoning}
}
func (t Tokens) Valid() bool {
	return t.Input >= 0 && t.Cached >= 0 && t.Write5m >= 0 && t.Write1h >= 0 && t.Output >= 0 && t.Reasoning >= 0 && t.Cached+t.Write5m+t.Write1h <= t.Input && t.Reasoning <= t.Output
}

type Event struct {
	Key     string    `json:"-"`
	Agent   string    `json:"agent"`
	Model   string    `json:"model"`
	Project string    `json:"project"`
	Session string    `json:"session"`
	Time    time.Time `json:"time"`
	Tokens  Tokens    `json:"tokens"`
}
type Quota struct {
	Bucket     string    `json:"bucket"`
	Window     string    `json:"window"`
	Used       float64   `json:"used_percent"`
	Minutes    int       `json:"window_minutes"`
	Reset      int64     `json:"resets_at"`
	Observed   time.Time `json:"observed_at"`
	AgeSeconds int64     `json:"age_seconds"`
	Stale      bool      `json:"stale"`
	Plan       string    `json:"plan"`
}
type Diagnostics struct {
	Files         int      `json:"files"`
	CachedFiles   int      `json:"cached_files"`
	Malformed     int      `json:"malformed_records"`
	Partial       int      `json:"partial_lines"`
	Duplicates    int      `json:"duplicates"`
	Regressions   int      `json:"counter_regressions"`
	Resets        int      `json:"inferred_counter_resets"`
	MissingTotals int      `json:"missing_totals"`
	MissingTime   int      `json:"missing_timestamps"`
	Reconciled    int      `json:"reconciled_sessions"`
	Warnings      []string `json:"warnings"`
}
type Ledger struct {
	Events      []Event
	Quotas      []Quota
	Diagnostics Diagnostics
}

func hash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:16] }
func label(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 120 {
		r = r[:120]
	}
	if len(r) == 0 {
		return "unknown"
	}
	return string(r)
}
