package meter

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Source struct {
	Agent string `json:"agent"`
	Root  string `json:"root"`
}
type cached struct {
	Size     int64
	Modified time.Time
	Data     Ledger
}
type Scanner struct {
	Sources []Source
	cache   map[string]cached
}

func NewScanner(sources []Source) *Scanner {
	return &Scanner{Sources: sources, cache: map[string]cached{}}
}
func DefaultSources(codex, claude string) []Source {
	home, _ := os.UserHomeDir()
	if codex == "" {
		codex = os.Getenv("CODEX_HOME")
	}
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	sources := []Source{{"codex", filepath.Join(codex, "sessions")}, {"codex", filepath.Join(codex, "archived_sessions")}}
	if claude == "" {
		claude = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if claude != "" {
		sources = append(sources, Source{"claude", filepath.Join(claude, "projects")})
	} else {
		sources = append(sources, Source{"claude", filepath.Join(home, ".claude", "projects")}, Source{"claude", filepath.Join(home, ".config", "claude", "projects")})
	}
	return sources
}
func (s *Scanner) Scan() Ledger {
	out := Ledger{}
	alive := map[string]bool{}
	events := map[string]Event{}
	quotas := map[string]Quota{}
	for _, source := range s.Sources {
		root, err := filepath.EvalSymlinks(source.Root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, source.Agent+": cannot access source")
			continue
		}
		// Root-scoped Open prevents symlink escapes even if files change between discovery and opening.
		safe, err := os.OpenRoot(root)
		if err != nil {
			out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, source.Agent+": cannot open source")
			continue
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, source.Agent+": cannot read a directory")
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".jsonl") || (source.Agent == "codex" && !strings.HasPrefix(d.Name(), "rollout-")) {
				return nil
			}
			stat, e := d.Info()
			if e != nil || !stat.Mode().IsRegular() {
				return nil
			}
			alive[path] = true
			out.Diagnostics.Files++
			c, ok := s.cache[path]
			if ok && c.Size == stat.Size() && c.Modified.Equal(stat.ModTime()) {
				out.Diagnostics.CachedFiles++
			} else {
				rel, e := filepath.Rel(root, path)
				if e != nil {
					return nil
				}
				f, e := safe.Open(rel)
				if e != nil {
					out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, source.Agent+": cannot read a log")
					return nil
				}
				c = cached{stat.Size(), stat.ModTime(), Parse(f, source.Agent, path)}
				f.Close()
				s.cache[path] = c
			}
			dgn := c.Data.Diagnostics
			out.Diagnostics.Malformed += dgn.Malformed
			out.Diagnostics.Partial += dgn.Partial
			out.Diagnostics.Duplicates += dgn.Duplicates
			out.Diagnostics.Regressions += dgn.Regressions
			out.Diagnostics.Resets += dgn.Resets
			out.Diagnostics.MissingTotals += dgn.MissingTotals
			out.Diagnostics.MissingTime += dgn.MissingTime
			out.Diagnostics.Reconciled += dgn.Reconciled
			out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, dgn.Warnings...)
			for _, ev := range c.Data.Events {
				old, exists := events[ev.Key]
				if exists {
					out.Diagnostics.Duplicates++
					if ev.Tokens.Total() <= old.Tokens.Total() {
						continue
					}
				}
				events[ev.Key] = ev
			}
			for _, q := range c.Data.Quotas {
				key := q.Bucket + ":" + q.Window
				old, exists := quotas[key]
				if !exists || q.Observed.After(old.Observed) {
					quotas[key] = q
				}
			}
			return nil
		})
		safe.Close()
		if err != nil {
			out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, source.Agent+": scan incomplete")
		}
	}
	for path := range s.cache {
		if !alive[path] {
			delete(s.cache, path)
		}
	}
	for _, ev := range events {
		out.Events = append(out.Events, ev)
	}
	sort.Slice(out.Events, func(i, j int) bool {
		if out.Events[i].Time.Equal(out.Events[j].Time) {
			return out.Events[i].Key < out.Events[j].Key
		}
		return out.Events[i].Time.Before(out.Events[j].Time)
	})
	for _, q := range quotas {
		if q.Used >= 0 {
			out.Quotas = append(out.Quotas, q)
		}
	}
	sort.Slice(out.Quotas, func(i, j int) bool {
		return out.Quotas[i].Bucket+out.Quotas[i].Window < out.Quotas[j].Bucket+out.Quotas[j].Window
	})
	return out
}
