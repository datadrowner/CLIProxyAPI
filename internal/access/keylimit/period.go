package keylimit

import (
	"fmt"
	"strings"
	"time"
)

type periodKind int

const (
	periodTotal periodKind = iota
	periodDaily
	periodWeekly
	periodMonthly
	periodFixed
)

// period describes when a token budget resets. Calendar periods use UTC.
type period struct {
	raw      string
	kind     periodKind
	duration time.Duration
}

func parsePeriod(raw string) (period, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	p := period{raw: normalized}
	switch normalized {
	case "", "total", "lifetime", "none":
		p.raw = ""
		p.kind = periodTotal
	case "daily", "day":
		p.kind = periodDaily
	case "weekly", "week":
		p.kind = periodWeekly
	case "monthly", "month":
		p.kind = periodMonthly
	default:
		d, errParse := time.ParseDuration(normalized)
		if errParse != nil || d <= 0 {
			return period{raw: normalized}, fmt.Errorf("period %q must be total, daily, weekly, monthly, or a positive duration like 5h", raw)
		}
		p.kind = periodFixed
		p.duration = d
	}
	return p, nil
}

func (p period) label() string {
	if p.raw == "" {
		return "total"
	}
	return p.raw
}

// windowStart returns the start of the window containing now. current is the
// start of the window in use; fixed-duration windows stay anchored to it until
// they expire and then restart at now.
func (p period) windowStart(now, current time.Time) time.Time {
	now = now.UTC()
	switch p.kind {
	case periodDaily:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case periodWeekly:
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(day.Weekday()) + 6) % 7 // Monday starts the week.
		return day.AddDate(0, 0, -offset)
	case periodMonthly:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	case periodFixed:
		if !current.IsZero() && now.Before(current.Add(p.duration)) {
			return current
		}
		return now
	default:
		return time.Time{}
	}
}

// resetAt returns when the window starting at start ends; zero for total budgets.
func (p period) resetAt(start time.Time) time.Time {
	if start.IsZero() {
		return time.Time{}
	}
	switch p.kind {
	case periodDaily:
		return start.AddDate(0, 0, 1)
	case periodWeekly:
		return start.AddDate(0, 0, 7)
	case periodMonthly:
		return start.AddDate(0, 1, 0)
	case periodFixed:
		return start.Add(p.duration)
	default:
		return time.Time{}
	}
}
