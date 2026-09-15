package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/MehrdadAlvandipour/burn-o-meter-go/internal/meter"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "burn:", err)
		os.Exit(1)
	}
}
func run() error {
	command := "today"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "today", "models", "daily", "projects", "doctor", "watch":
	case "help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q; use burn help", command)
	}
	fs := flag.NewFlagSet("burn "+command, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON report")
	sinceFlag := fs.String("since", "", "today, all, Nd, or YYYY-MM-DD")
	codex := fs.String("codex-home", "", "Codex home (default CODEX_HOME or ~/.codex)")
	claude := fs.String("claude-home", "", "Claude config home")
	agent := fs.String("agent", "all", "all, codex, or claude")
	pricesFile := fs.String("prices", os.Getenv("BURN_PRICES"), "custom JSON price overrides")
	interval := fs.Duration("interval", 15*time.Second, "watch refresh interval (minimum 2s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *agent != "all" && *agent != "codex" && *agent != "claude" {
		return fmt.Errorf("invalid agent")
	}
	prices, err := meter.Prices(*pricesFile)
	if err != nil {
		return err
	}
	sources := meter.DefaultSources(*codex, *claude)
	scanner := meter.NewScanner(sources)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *interval < 2*time.Second {
		return fmt.Errorf("interval must be at least 2s")
	}
	for {
		now := time.Now()
		period := *sinceFlag
		if period == "" {
			period = "today"
			if command == "models" || command == "daily" || command == "projects" {
				period = "30d"
			}
		}
		since, err := start(period, now)
		if err != nil {
			return err
		}
		r := meter.Build(scanner.Scan(), since, now, prices, *agent)
		if *asJSON || command == "watch" {
			if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
				return err
			}
		} else {
			if command == "doctor" {
				fmt.Println("Sources (missing roots are normal when an agent is unused):")
				for _, s := range sources {
					_, err := os.Stat(s.Root)
					status := "found"
					if err != nil {
						status = "unavailable"
					}
					fmt.Printf("  %s: %s [%s]\n", s.Agent, s.Root, status)
				}
				b, _ := json.MarshalIndent(r.Diagnostics, "", "  ")
				fmt.Println(string(b))
				fmt.Println(r.Scope)
			} else {
				printReport(r, command)
			}
		}
		if command != "watch" {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}
func start(s string, now time.Time) (time.Time, error) {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if s == "today" {
		return midnight, nil
	}
	if s == "all" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 && n <= 36500 {
			return midnight.AddDate(0, 0, -n+1), nil
		}
	}
	t, err := time.ParseInLocation("2006-01-02", s, now.Location())
	if err != nil {
		return t, fmt.Errorf("invalid --since; use today, all, Nd or YYYY-MM-DD")
	}
	return t, nil
}
func printReport(r meter.Report, command string) {
	fmt.Printf("Burn · %s → %s\n", r.Since.Format("2006-01-02"), r.Generated.Format("2006-01-02 15:04"))
	rows := r.Models
	if command == "daily" {
		rows = r.Daily
	}
	if command == "projects" {
		rows = r.Projects
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tUPDATES\tTOKENS\tCACHE HIT\tAPI VALUE")
	for _, row := range rows {
		value := fmt.Sprintf("~$%.2f", row.KnownValue)
		if row.Unpriced == row.Updates {
			value = "—"
		} else if row.Unpriced > 0 {
			value += " + unpriced"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%.1f%%\t%s\n", row.Name, row.Updates, row.Total, row.CachePercent, value)
	}
	w.Flush()
	fmt.Printf("\n%d tokens · %d usage updates · ~$%.2f known-price subtotal", r.Summary.Total, r.Summary.Updates, r.Summary.KnownValue)
	if r.Summary.Unpriced > 0 {
		fmt.Printf(" · %d unpriced updates", r.Summary.Unpriced)
	}
	fmt.Println("\n" + r.Basis)
	for _, q := range r.Quotas {
		stale := ""
		if q.Stale {
			stale = " · STALE"
		}
		reset := "unknown"
		if q.Reset > 0 {
			reset = time.Unix(q.Reset, 0).Local().Format("Jan 02 15:04")
		}
		fmt.Printf("Codex %s / %s: %.1f%% used · %dm window · resets %s · observed %ds ago%s\n", q.Bucket, q.Window, q.Used, q.Minutes, reset, q.AgeSeconds, stale)
	}
	d := r.Diagnostics
	if d.Malformed+d.Regressions+d.MissingTime > 0 || len(d.Warnings) > 0 {
		fmt.Println("⚠ Some records could not be safely counted; run burn doctor.")
	}
	if r.Summary.Updates == 0 {
		fmt.Println("No recorded usage in this period. Run burn doctor to check sources.")
	}
}
func usage() {
	fmt.Println(`Burn — local Codex and Claude usage, powered by Go

burn today [--json]                 Today's recorded usage and Codex quota
burn models --since 30d             Per-model tokens and estimated API value
burn daily --since 14d              Calendar-day totals
burn projects --since all           Project totals (basename + stable hash)
burn doctor                        Sources and accounting diagnostics
burn watch --interval 15s           Newline-delimited JSON for the macOS app

Shared flags: --agent all|codex|claude --codex-home PATH --claude-home PATH
              --prices FILE --since today|all|Nd|YYYY-MM-DD --json

No network, credentials, transcript storage, or installation required.`)
}
