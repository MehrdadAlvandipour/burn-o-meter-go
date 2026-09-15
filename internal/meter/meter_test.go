package meter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const contextLine = `{"type":"session_meta","payload":{"id":"session-a","cwd":"/private/project"}}
{"type":"turn_context","payload":{"model":"model-a"}}
`

func event(sec int, input, cached, output, reason int64, extra string) string {
	return fmt.Sprintf(`{"timestamp":"2026-09-14T12:00:%02dZ","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}%s}}}`+"\n", sec, input, cached, output, reason, input+output, extra)
}
func sum(l Ledger) Tokens {
	var t Tokens
	for _, e := range l.Events {
		t = t.Add(e.Tokens)
	}
	return t
}
func TestCodexCumulativeDuplicateModelSwitch(t *testing.T) {
	a := event(1, 100, 60, 20, 5, "")
	l := Parse(strings.NewReader(contextLine+a+a+`{"type":"turn_context","payload":{"model":"model-b"}}`+"\n"+event(2, 150, 80, 30, 8, "")), "codex", "test")
	if got := sum(l); got != (Tokens{Input: 150, Cached: 80, Output: 30, Reasoning: 8}) {
		t.Fatalf("double counted/subset error: %+v", got)
	}
	if len(l.Events) != 2 || l.Events[1].Model != "model-b" || l.Diagnostics.Reconciled != 1 {
		t.Fatalf("bad attribution or reconciliation: %+v", l)
	}
}
func TestResetVersusStaleRegression(t *testing.T) {
	reset := `,"last_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0,"total_tokens":12}`
	l := Parse(strings.NewReader(contextLine+event(1, 100, 60, 20, 5, "")+event(2, 10, 0, 2, 0, reset)+event(3, 30, 10, 8, 2, "")), "codex", "test")
	if sum(l).Total() != 158 || l.Diagnostics.Resets != 1 || l.Diagnostics.Reconciled != 1 {
		t.Fatalf("reset lost usage: %+v", l)
	}
	stale := Parse(strings.NewReader(contextLine+event(2, 100, 60, 20, 5, "")+event(1, 10, 0, 2, 0, reset)+event(3, 150, 80, 30, 8, "")), "codex", "test")
	if sum(stale).Total() != 180 || stale.Diagnostics.Regressions != 1 {
		t.Fatalf("stale event inflated usage: %+v", stale)
	}
}
func TestPartialMalformedAndInvalidSubsets(t *testing.T) {
	s := contextLine + "{bad json}\n" + event(1, 100, 150, 10, 0, "") + event(2, 100, 50, 10, 0, "") + strings.TrimSuffix(event(3, 200, 60, 20, 0, ""), "\n")
	l := Parse(strings.NewReader(s), "codex", "test")
	if sum(l).Total() != 110 || l.Diagnostics.Malformed != 2 || l.Diagnostics.Partial != 1 {
		t.Fatalf("bad tail handling: %+v", l)
	}
}
func TestMissingTimeNotQuietlyReconciled(t *testing.T) {
	s := strings.Replace(event(1, 100, 50, 10, 0, ""), "2026-09-14T12:00:01Z", "bad", 1)
	l := Parse(strings.NewReader(contextLine+s+event(2, 150, 60, 20, 0, "")), "codex", "test")
	if l.Diagnostics.Reconciled != 0 || l.Diagnostics.MissingTime != 1 || sum(l).Total() != 60 {
		t.Fatalf("misleading reconciliation: %+v", l)
	}
}
func TestClaudeCacheAndPrivacy(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-09-14T12:00:01Z","sessionId":"s","cwd":"/private/project","message":{"id":"m","model":"claude-test","content":[{"text":"SECRET_CANARY"}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":10}}}}` + "\n"
	l := Parse(strings.NewReader(line), "claude", "test")
	if sum(l).Total() != 210 || sum(l).Write1h != 10 {
		t.Fatalf("cache normalization wrong: %+v", l)
	}
	b, _ := json.Marshal(l)
	if strings.Contains(string(b), "SECRET_CANARY") || strings.Contains(string(b), "/private") {
		t.Fatal("retained transcript content/path")
	}
	p := Price{Input: 1, Cached: .1, Write5m: 1.25, Write1h: 2, Output: 5}
	if got := p.Cost(sum(l)); got < .00026249 || got > .00026251 {
		t.Fatalf("wrong price: %f", got)
	}
}
func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestScannerArchiveForkCacheTruncationAndSymlink(t *testing.T) {
	root := t.TempDir()
	archive := t.TempDir()
	a := contextLine + event(1, 100, 50, 20, 0, "")
	path := filepath.Join(root, "rollout-a.jsonl")
	write(t, path, a)
	// A copied/forked history changes session id; original timestamps and totals are identical.
	write(t, filepath.Join(archive, "rollout-fork.jsonl"), strings.ReplaceAll(a, "session-a", "session-b"))
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	write(t, outside, contextLine+event(4, 9999, 0, 99, 0, ""))
	if err := os.Symlink(outside, filepath.Join(root, "rollout-escape.jsonl")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "auth.json"), "SECRET_CANARY")
	s := NewScanner([]Source{{"codex", root}, {"codex", archive}})
	l := s.Scan()
	if sum(l).Total() != 120 || l.Diagnostics.Files != 2 {
		t.Fatalf("duplication or escaped source: %+v", l)
	}
	if s.Scan().Diagnostics.CachedFiles != 2 {
		t.Fatal("unchanged files reparsed")
	}
	write(t, path, contextLine+event(2, 200, 100, 40, 0, ""))
	if sum(s.Scan()).Total() != 360 {
		t.Fatal("rewrite did not invalidate cache")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if sum(s.Scan()).Total() != 120 {
		t.Fatal("deleted source persisted")
	}
}
func TestQuotaLatestBucketReplacesNullWindows(t *testing.T) {
	root := t.TempDir()
	first := `{"type":"event_msg","timestamp":"2026-09-14T12:00:01Z","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","plan_type":"old","primary":{"used_percent":30,"window_minutes":300},"secondary":{"used_percent":50,"window_minutes":10080}}}}` + "\n"
	next := `{"type":"event_msg","timestamp":"2026-09-14T12:00:02Z","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","plan_type":"new","primary":{"used_percent":3,"window_minutes":10080},"secondary":null}}}` + "\n"
	other := strings.ReplaceAll(first, `"limit_id":"codex"`, `"limit_id":"other"`)
	write(t, filepath.Join(root, "rollout-a.jsonl"), first+next+other)
	l := NewScanner([]Source{{"codex", root}}).Scan()
	if len(l.Quotas) != 3 {
		t.Fatalf("stale secondary survived or bucket lost: %+v", l.Quotas)
	}
	for _, q := range l.Quotas {
		if q.Bucket == "codex" && (q.Plan != "new" || q.Used != 3 || q.Minutes != 10080) {
			t.Fatalf("wrong quota %+v", q)
		}
	}
}
func TestReportTimezoneUnknownPriceAndStaleness(t *testing.T) {
	loc, _ := time.LoadLocation("America/Los_Angeles")
	now := time.Date(2026, 9, 14, 23, 0, 0, 0, loc)
	e := Event{Agent: "codex", Model: "unknown", Time: now, Tokens: Tokens{Input: 100, Output: 10}}
	l := Ledger{Events: []Event{e}, Quotas: []Quota{{Observed: now.Add(-time.Hour), Used: 5}}}
	r := Build(l, time.Date(2026, 9, 14, 0, 0, 0, 0, loc), now, nil, "all")
	if r.Daily[0].Name != "2026-09-14" || r.Summary.Unpriced != 1 || !r.Quotas[0].Stale || len(r.UnpricedModels) != 1 {
		t.Fatalf("report error %+v", r)
	}
	if len(Build(l, time.Time{}, now, nil, "claude").Quotas) != 0 {
		t.Fatal("wrong provider quota")
	}
}
func TestPricesRejectNegativeAndRequireSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	for _, v := range []string{`{"m":{"input":-1,"source":"test"}}`, `{"m":{"input":1}}`, `bad`} {
		write(t, path, v)
		if _, err := Prices(path); err == nil {
			t.Fatalf("accepted invalid prices: %s", v)
		}
	}
}
func FuzzParse(f *testing.F) {
	f.Add(contextLine + event(1, 100, 50, 10, 0, ""))
	f.Add("{}\n")
	f.Fuzz(func(t *testing.T, s string) {
		l := Parse(strings.NewReader(s), "codex", "fuzz")
		for _, e := range l.Events {
			if !e.Tokens.Valid() {
				t.Fatal("invalid tokens escaped parser")
			}
		}
	})
}

func TestOversizedLineDoesNotHideFollowingUsage(t *testing.T) {
	l := Parse(strings.NewReader(strings.Repeat("x", 9*1024*1024)+"\n"+contextLine+event(1, 100, 50, 10, 0, "")), "codex", "test")
	if l.Diagnostics.Malformed != 1 || sum(l).Total() != 110 {
		t.Fatal("oversized line broke scan")
	}
}
func TestCustomPriceOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	write(t, path, `{"model-a":{"input":1,"cached":0.1,"write_5m":1.25,"write_1h":2,"output":5,"source":"test"}}`)
	p, err := Prices(path)
	if err != nil || p["model-a"].Input != 1 {
		t.Fatalf("override failed: %v", err)
	}
}
