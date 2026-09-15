package main

import (
	"testing"
	"time"
)

func TestCalendarDaysAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 9, 12, 0, 0, 0, loc)
	since, err := start("2d", now)
	if err != nil {
		t.Fatal(err)
	}
	if since.Day() != 8 || since.Hour() != 0 {
		t.Fatal(since)
	}
	for _, s := range []string{"0d", "-2d", "garbage", "100000d"} {
		if _, err := start(s, now); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
