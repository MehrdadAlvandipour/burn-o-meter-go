package meter

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

type Price struct {
	Input   float64 `json:"input"`
	Cached  float64 `json:"cached"`
	Write5m float64 `json:"write_5m"`
	Write1h float64 `json:"write_1h"`
	Output  float64 `json:"output"`
	Source  string  `json:"source"`
}

// Baseline standard/short-context API-equivalent rates, NOT a billing calculator.
// Exact model matches only: aliases and unknown models never inherit a guessed price.
func Prices(path string) (map[string]Price, error) {
	src := "OpenAI standard short-context list, 2026-09-14; https://developers.openai.com/api/docs/pricing"
	p := map[string]Price{
		"gpt-6-astra":   {10, 1, 12.5, 12.5, 50, src},
		"gpt-5.6-sol":   {4, .4, 5, 5, 20, src},
		"gpt-5.6-terra": {2, .2, 2.5, 2.5, 12, src},
		"gpt-5.6-luna":  {.2, .02, .25, .25, 1.2, src},
		"gpt-5.3-codex": {1.75, .175, 1.75, 1.75, 14, src},
	}
	if path == "" {
		return p, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var custom map[string]Price
	if err = json.Unmarshal(raw, &custom); err != nil {
		return nil, fmt.Errorf("invalid price file: %w", err)
	}
	var fields map[string]map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("invalid price entries")
	}
	for _, entry := range fields {
		for _, name := range []string{"input", "cached", "write_5m", "write_1h", "output", "source"} {
			if value, ok := entry[name]; !ok || string(value) == "null" {
				return nil, fmt.Errorf("each price must explicitly specify %s", name)
			}
		}
	}
	for model, v := range custom {
		if v.Input < 0 || v.Cached < 0 || v.Write5m < 0 || v.Write1h < 0 || v.Output < 0 || v.Source == "" {
			return nil, fmt.Errorf("prices must be nonnegative and include a source")
		}
		p[model] = v
	}
	return p, nil
}
func (p Price) Cost(t Tokens) float64 {
	return (float64(t.Input-t.Cached-t.Write5m-t.Write1h)*p.Input + float64(t.Cached)*p.Cached + float64(t.Write5m)*p.Write5m + float64(t.Write1h)*p.Write1h + float64(t.Output)*p.Output) / 1e6
}

type Row struct {
	Name         string   `json:"name"`
	Updates      int      `json:"usage_updates"`
	Tokens       Tokens   `json:"tokens"`
	Total        int64    `json:"total_tokens"`
	CachePercent float64  `json:"cache_percent"`
	KnownValue   float64  `json:"known_value_usd"`
	Unpriced     int      `json:"unpriced_updates"`
	Sources      []string `json:"price_sources"`
}
type Report struct {
	Version        int         `json:"schema_version"`
	Generated      time.Time   `json:"generated_at"`
	Since          time.Time   `json:"since"`
	Scope          string      `json:"scope"`
	Basis          string      `json:"cost_basis"`
	Summary        Row         `json:"summary"`
	Models         []Row       `json:"models"`
	Daily          []Row       `json:"daily"`
	Projects       []Row       `json:"projects"`
	Quotas         []Quota     `json:"quotas"`
	Diagnostics    Diagnostics `json:"diagnostics"`
	UnpricedModels []string    `json:"unpriced_models"`
}

func addRow(row *Row, e Event, prices map[string]Price) {
	row.Updates++
	row.Tokens = row.Tokens.Add(e.Tokens)
	row.Total = row.Tokens.Total()
	if row.Tokens.Input > 0 {
		row.CachePercent = 100 * float64(row.Tokens.Cached) / float64(row.Tokens.Input)
	}
	if p, ok := prices[e.Model]; ok {
		row.KnownValue += p.Cost(e.Tokens)
		found := false
		for _, s := range row.Sources {
			if s == p.Source {
				found = true
			}
		}
		if !found {
			row.Sources = append(row.Sources, p.Source)
		}
	} else {
		row.Unpriced++
	}
}
func Build(l Ledger, since, now time.Time, prices map[string]Price, agent string) Report {
	r := Report{Version: 1, Generated: now, Since: since, Scope: "Local logs only; remote/cloud/other devices may be absent", Basis: "Baseline API-equivalent estimate, not billed spend; standard short-context rates", Summary: Row{Name: "Total"}, Diagnostics: l.Diagnostics, Models: []Row{}, Daily: []Row{}, Projects: []Row{}, Quotas: []Quota{}, UnpricedModels: []string{}}
	models, days, projects := map[string]*Row{}, map[string]*Row{}, map[string]*Row{}
	unpriced := map[string]bool{}
	for _, e := range l.Events {
		if e.Time.Before(since) || e.Time.After(now) || (agent != "all" && agent != "" && e.Agent != agent) {
			continue
		}
		addRow(&r.Summary, e, prices)
		for _, group := range []struct {
			m map[string]*Row
			k string
		}{{models, e.Agent + " / " + e.Model}, {days, e.Time.In(now.Location()).Format("2006-01-02")}, {projects, e.Project}} {
			if group.m[group.k] == nil {
				group.m[group.k] = &Row{Name: group.k}
			}
			addRow(group.m[group.k], e, prices)
		}
		if _, ok := prices[e.Model]; !ok {
			unpriced[e.Model] = true
		}
	}
	for _, g := range []struct {
		m    map[string]*Row
		dest *[]Row
	}{{models, &r.Models}, {days, &r.Daily}, {projects, &r.Projects}} {
		for _, row := range g.m {
			*g.dest = append(*g.dest, *row)
		}
		sort.Slice(*g.dest, func(i, j int) bool { return (*g.dest)[i].Name < (*g.dest)[j].Name })
	}
	for name := range unpriced {
		r.UnpricedModels = append(r.UnpricedModels, name)
	}
	sort.Strings(r.UnpricedModels)
	if agent != "claude" {
		for _, q := range l.Quotas {
			q.AgeSeconds = int64(now.Sub(q.Observed).Seconds())
			q.Stale = q.AgeSeconds > 900 || q.AgeSeconds < 0 || (q.Reset > 0 && q.Reset <= now.Unix())
			r.Quotas = append(r.Quotas, q)
		}
	}
	return r
}
