package meter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type counters struct {
	Input     int64  `json:"input_tokens"`
	Cached    int64  `json:"cached_input_tokens"`
	Write     int64  `json:"cache_write_input_tokens"`
	Output    int64  `json:"output_tokens"`
	Reasoning int64  `json:"reasoning_output_tokens"`
	Total     *int64 `json:"total_tokens"`
}

func (c counters) tokens() Tokens {
	return Tokens{Input: c.Input, Cached: c.Cached, Write5m: c.Write, Output: c.Output, Reasoning: c.Reasoning}
}

type record struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Session   string          `json:"sessionId"`
	CWD       string          `json:"cwd"`
	Request   string          `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input    int64 `json:"input_tokens"`
			Output   int64 `json:"output_tokens"`
			Read     int64 `json:"cache_read_input_tokens"`
			Write    int64 `json:"cache_creation_input_tokens"`
			Creation *struct {
				Short int64 `json:"ephemeral_5m_input_tokens"`
				Long  int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}
type payload struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model"`
	CWD   string `json:"cwd"`
	Fork  string `json:"forked_from_id"`
	Info  *struct {
		Total *counters `json:"total_token_usage"`
		Last  *counters `json:"last_token_usage"`
	} `json:"info"`
	Limits *struct {
		ID        string  `json:"limit_id"`
		Plan      string  `json:"plan_type"`
		Primary   *window `json:"primary"`
		Secondary *window `json:"secondary"`
	} `json:"rate_limits"`
}
type window struct {
	Used    *float64 `json:"used_percent"`
	Minutes int      `json:"window_minutes"`
	Reset   int64    `json:"resets_at"`
}

// Parse only commits newline-terminated records. An in-progress tail is retried next scan.
// JSON content passes through memory but only the typed metadata above is retained.
func Parse(r io.Reader, agent, identity string) Ledger {
	out := Ledger{}
	br := bufio.NewReaderSize(r, 64*1024)
	session, model, project := hash(identity), "unknown", "unknown"
	var prev, summed, banked Tokens
	var prevTime time.Time
	validTotals := false
	for {
		raw, oversized, err := readLine(br)
		if err != nil {
			if len(raw) > 0 {
				out.Diagnostics.Partial++
			}
			if err != io.EOF {
				out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, "Log read failed")
			}
			break
		}
		if oversized {
			out.Diagnostics.Malformed++
			continue
		}
		var rec record
		if json.Unmarshal(raw, &rec) != nil {
			out.Diagnostics.Malformed++
			continue
		}
		ts, tsErr := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if agent == "claude" {
			u := rec.Message.Usage
			if rec.Type != "assistant" || u == nil || rec.Message.Model == "<synthetic>" {
				continue
			}
			if tsErr != nil {
				out.Diagnostics.MissingTime++
				continue
			}
			if rec.Message.ID == "" {
				out.Diagnostics.Malformed++
				continue
			}
			w5, w1 := u.Write, int64(0)
			if u.Creation != nil {
				w5 = u.Creation.Short
				w1 = u.Creation.Long
			}
			t := Tokens{Input: u.Input + u.Read + w5 + w1, Cached: u.Read, Write5m: w5, Write1h: w1, Output: u.Output}
			if !t.Valid() {
				out.Diagnostics.Malformed++
				continue
			}
			out.Events = append(out.Events, Event{Key: "claude:" + rec.Message.ID, Agent: agent, Model: label(rec.Message.Model), Project: projectLabel(rec.CWD), Session: hash(rec.Session), Time: ts, Tokens: t})
			continue
		}
		// Avoid decoding unrelated payloads, including conversation messages.
		if rec.Type != "session_meta" && rec.Type != "turn_context" && rec.Type != "event_msg" {
			continue
		}
		var p payload
		if json.Unmarshal(rec.Payload, &p) != nil {
			out.Diagnostics.Malformed++
			continue
		}
		switch rec.Type {
		case "session_meta":
			if p.ID != "" {
				session = hash(p.ID)
			}
			if p.CWD != "" {
				project = projectLabel(p.CWD)
			}
		case "turn_context":
			if p.Model != "" {
				model = label(p.Model)
			}
			if p.CWD != "" {
				project = projectLabel(p.CWD)
			}
		case "event_msg":
			if p.Type != "token_count" {
				continue
			}
			if p.Limits != nil && tsErr == nil {
				bucket := label(p.Limits.ID)
				if p.Limits.ID == "" {
					bucket = "codex"
				}
				for _, v := range []struct {
					name string
					w    *window
				}{{"primary", p.Limits.Primary}, {"secondary", p.Limits.Secondary}} {
					if v.w == nil || v.w.Used == nil || *v.w.Used < 0 || *v.w.Used > 100 {
						out.Quotas = append(out.Quotas, Quota{Bucket: bucket, Window: v.name, Used: -1, Observed: ts})
						continue
					}
					out.Quotas = append(out.Quotas, Quota{Bucket: bucket, Window: v.name, Used: *v.w.Used, Minutes: v.w.Minutes, Reset: v.w.Reset, Observed: ts, Plan: label(p.Limits.Plan)})
				}
			}
			if p.Info == nil || p.Info.Total == nil {
				out.Diagnostics.MissingTotals++
				continue
			}
			curr := p.Info.Total.tokens()
			if !curr.Valid() || (p.Info.Total.Total != nil && *p.Info.Total.Total != curr.Total()) {
				out.Diagnostics.Malformed++
				continue
			}
			delta := curr.Sub(prev)
			if delta == (Tokens{}) {
				out.Diagnostics.Duplicates++
				continue
			}
			// Infer a restart only if the new total equals the last request and time advances.
			if !delta.Valid() {
				if curr.Total() > 0 && curr.Input < prev.Input && curr.Output < prev.Output && p.Info.Last != nil && p.Info.Last.tokens() == curr && ts.After(prevTime) {
					banked = banked.Add(prev)
					delta = curr
					out.Diagnostics.Resets++
				} else {
					out.Diagnostics.Regressions++
					continue
				}
			}
			prev = curr
			prevTime = ts
			validTotals = true
			if tsErr != nil {
				out.Diagnostics.MissingTime++
				continue
			}
			summed = summed.Add(delta)
			// Timestamp + cumulative counters deduplicates inherited history in forked/copy rollouts,
			// even when the fork changes its session id. Independent requests have distinct timestamps.
			key := fmt.Sprintf("codex:%s:%v", rec.Timestamp, curr)
			out.Events = append(out.Events, Event{Key: key, Agent: agent, Model: model, Project: project, Session: session, Time: ts, Tokens: delta})
		}
	}
	if validTotals && summed == prev.Add(banked) && out.Diagnostics.Regressions == 0 {
		out.Diagnostics.Reconciled++
	}
	return out
}

// Bound transient memory for unusually large tool outputs; consume the whole
// oversized line so subsequent usage records remain readable.
func readLine(br *bufio.Reader) ([]byte, bool, error) {
	const maxLine = 8 * 1024 * 1024
	var line []byte
	oversized := false
	for {
		part, err := br.ReadSlice('\n')
		if len(line)+len(part) > maxLine {
			oversized = true
		}
		if !oversized {
			line = append(line, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, oversized, err
	}
}
func projectLabel(cwd string) string {
	if cwd == "" {
		return "unknown"
	}
	return label(filepath.Base(strings.TrimRight(cwd, "/\\"))) + " · " + hash(cwd)[:6]
}
