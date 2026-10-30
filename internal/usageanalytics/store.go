// Package usageanalytics aggregates token usage and its Anthropic API list-price
// equivalent per month, client API key, upstream credential and model. Client
// keys are stored as SHA-256 digests; the raw keys never reach the state file.
package usageanalytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultStateFileName is the analytics file name inside the auth directory.
	// It must not end in .json: every *.json file there is loaded as a credential.
	DefaultStateFileName = "usage-analytics.state"
	// legacyStateFileName is the previous default name; it is renamed on startup.
	legacyStateFileName = "usage-analytics.json"
	// retainedMonths bounds the state file size.
	retainedMonths = 13
	flushInterval  = 15 * time.Second
)

func init() {
	coreusage.RegisterNamedPlugin("usage-analytics", Default())
}

var defaultStore = New()

// Default returns the process-wide analytics store.
func Default() *Store { return defaultStore }

// Row aggregates usage for one month, client key, credential and model.
type Row struct {
	KeyID            string  `json:"key_id"`
	AuthIndex        string  `json:"auth_index,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	Failed           int64   `json:"failed"`
	InputTokens      int64   `json:"input_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	// UnpricedTokens counts tokens of models without a known API price.
	UnpricedTokens int64 `json:"unpriced_tokens"`
}

// Totals aggregates all usage of one day ("YYYY-MM-DD") or month ("YYYY-MM").
type Totals struct {
	Period         string  `json:"period"`
	Requests       int64   `json:"requests"`
	TotalTokens    int64   `json:"total_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	UnpricedTokens int64   `json:"unpriced_tokens"`
}

func (t *Totals) add(requests, tokens int64, costUSD float64, unpriced int64) {
	t.Requests += requests
	t.TotalTokens += tokens
	t.CostUSD += costUSD
	t.UnpricedTokens += unpriced
}

// Report is the analytics view for one month.
type Report struct {
	Month  string   `json:"month"`
	Months []string `json:"months"`
	Rows   []Row    `json:"rows"`
	// Days lists the recorded days of the month in date order. Days are
	// recorded since daily totals were added; earlier usage has none.
	Days []Totals `json:"days"`
	// Trend lists every retained month's totals, oldest first.
	Trend []Totals `json:"trend"`
	// Keys maps key IDs to configured names or masked keys.
	Keys          map[string]string `json:"keys"`
	Pricing       []Price           `json:"pricing"`
	PricingSource string            `json:"pricing_source"`
}

type stateFile struct {
	Version int                        `json:"version"`
	Months  map[string]map[string]*Row `json:"months"`
	// Days maps "YYYY-MM-DD" (UTC) to that day's totals.
	Days map[string]*Totals `json:"days,omitempty"`
}

// Store aggregates usage records in memory and persists them periodically.
type Store struct {
	mu     sync.Mutex
	months map[string]map[string]*Row
	days   map[string]*Totals
	labels map[string]string
	path   string
	dirty  bool

	saveMu    sync.Mutex
	flushOnce sync.Once
	now       func() time.Time
}

// New constructs an empty store.
func New() *Store {
	return &Store{months: make(map[string]map[string]*Row), days: make(map[string]*Totals), labels: make(map[string]string), now: time.Now}
}

// KeyID returns the identifier used for a client API key.
func KeyID(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:16]
}

// Configure sets key labels from cfg and loads the state file when its location changes.
func (s *Store) Configure(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	labels := make(map[string]string)
	for _, key := range cfg.APIKeys {
		if id := KeyID(key); id != "" {
			labels[id] = maskKey(strings.TrimSpace(key))
		}
	}
	for _, entry := range cfg.APIKeyLimits.Keys {
		id := KeyID(entry.Key)
		if id == "" {
			continue
		}
		label := strings.TrimSpace(entry.Name)
		if label == "" {
			label = maskKey(strings.TrimSpace(entry.Key))
		}
		labels[id] = label
	}
	path := resolveStatePath(cfg)

	s.mu.Lock()
	s.labels = labels
	pathChanged := path != s.path
	s.mu.Unlock()

	if pathChanged {
		s.Flush()
		loaded, errLoad := loadState(path)
		if errLoad != nil {
			log.Warnf("usage-analytics: failed to load %s: %v", path, errLoad)
			loaded = stateFile{Months: make(map[string]map[string]*Row), Days: make(map[string]*Totals)}
		}
		s.mu.Lock()
		if s.path == "" {
			// Keep records that arrived before the first configuration.
			mergeMonths(loaded.Months, s.months)
			mergeDays(loaded.Days, s.days)
		}
		s.path = path
		s.months = loaded.Months
		s.days = loaded.Days
		s.dirty = false
		s.mu.Unlock()
	}
	s.flushOnce.Do(func() { go s.flushLoop() })
}

func resolveStatePath(cfg *config.Config) string {
	authDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil || strings.TrimSpace(authDir) == "" {
		authDir = "."
	}
	path := filepath.Join(authDir, DefaultStateFileName)
	util.MigrateStateFile(filepath.Join(authDir, legacyStateFileName), path)
	return path
}

func (s *Store) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.Flush()
	}
}

// HandleUsage implements coreusage.Plugin.
func (s *Store) HandleUsage(_ context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	t := splitTokens(record.Detail)
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = strings.TrimSpace(record.ResponseModel)
	}
	if model == "" {
		model = "unknown"
	}
	price, priced := LookupPrice(model)
	if !priced {
		if alt, ok := LookupPrice(record.ResponseModel); ok {
			price, priced = alt, true
		}
	}
	fast := strings.EqualFold(strings.TrimSpace(record.Detail.ResponseServiceTier), "fast")

	at := record.RequestedAt
	if at.IsZero() {
		at = s.now()
	}
	month := at.UTC().Format("2006-01")
	keyID := KeyID(record.APIKey)
	authIndex := strings.TrimSpace(record.AuthIndex)
	rowKey := keyID + "|" + authIndex + "|" + model

	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.months[month]
	if rows == nil {
		rows = make(map[string]*Row)
		s.months[month] = rows
		s.pruneLocked()
	}
	row := rows[rowKey]
	if row == nil {
		row = &Row{KeyID: keyID, AuthIndex: authIndex, Model: model}
		rows[rowKey] = row
	}
	if provider := strings.TrimSpace(record.Provider); provider != "" {
		row.Provider = provider
	}
	row.Requests++
	if record.Failed {
		row.Failed++
	}
	row.InputTokens += t.Input
	row.CacheReadTokens += t.CacheRead
	row.CacheWriteTokens += t.CacheWrite
	row.OutputTokens += t.Output
	row.ReasoningTokens += t.Reasoning
	row.TotalTokens += t.Total
	costUSD, unpriced := 0.0, int64(0)
	if priced {
		costUSD = cost(price, t, fast)
	} else {
		unpriced = t.Total
	}
	row.CostUSD += costUSD
	row.UnpricedTokens += unpriced
	day := at.UTC().Format("2006-01-02")
	if s.days[day] == nil {
		s.days[day] = &Totals{Period: day}
	}
	s.days[day].add(1, t.Total, costUSD, unpriced)
	s.dirty = true
}

func (s *Store) pruneLocked() {
	if len(s.months) <= retainedMonths {
		return
	}
	months := make([]string, 0, len(s.months))
	for month := range s.months {
		months = append(months, month)
	}
	sort.Strings(months)
	for _, month := range months[:len(months)-retainedMonths] {
		delete(s.months, month)
	}
	for day := range s.days {
		if _, ok := s.months[day[:7]]; !ok {
			delete(s.days, day)
		}
	}
}

// Report returns the rows of month ("YYYY-MM"); empty selects the current month.
func (s *Store) Report(month string) Report {
	month = strings.TrimSpace(month)
	if month == "" {
		month = s.now().UTC().Format("2006-01")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	report := Report{Month: month, Months: []string{}, Rows: []Row{}, Keys: make(map[string]string, len(s.labels)), Pricing: Prices, PricingSource: PricingSource}
	for m := range s.months {
		report.Months = append(report.Months, m)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(report.Months)))
	for _, row := range s.months[month] {
		report.Rows = append(report.Rows, *row)
	}
	sort.Slice(report.Rows, func(i, j int) bool { return report.Rows[i].CostUSD > report.Rows[j].CostUSD })
	report.Days = []Totals{}
	for day, totals := range s.days {
		if strings.HasPrefix(day, month+"-") {
			report.Days = append(report.Days, *totals)
		}
	}
	sort.Slice(report.Days, func(i, j int) bool { return report.Days[i].Period < report.Days[j].Period })
	report.Trend = make([]Totals, 0, len(report.Months))
	for i := len(report.Months) - 1; i >= 0; i-- {
		totals := Totals{Period: report.Months[i]}
		for _, row := range s.months[report.Months[i]] {
			totals.add(row.Requests, row.TotalTokens, row.CostUSD, row.UnpricedTokens)
		}
		report.Trend = append(report.Trend, totals)
	}
	for id, label := range s.labels {
		report.Keys[id] = label
	}
	return report
}

// Flush writes pending changes to the state file.
func (s *Store) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.dirty || s.path == "" {
		s.mu.Unlock()
		return
	}
	data, errMarshal := json.Marshal(stateFile{Version: 1, Months: s.months, Days: s.days})
	path := s.path
	s.dirty = false
	s.mu.Unlock()
	if errMarshal != nil {
		log.Warnf("usage-analytics: failed to encode state: %v", errMarshal)
		return
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		log.Warnf("usage-analytics: failed to create state directory: %v", errMkdir)
		return
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		log.Warnf("usage-analytics: failed to write state: %v", errWrite)
		return
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		log.Warnf("usage-analytics: failed to replace state: %v", errRename)
	}
}

func loadState(path string) (stateFile, error) {
	state := stateFile{}
	data, errRead := os.ReadFile(path)
	if errRead != nil && !errors.Is(errRead, os.ErrNotExist) {
		return state, errRead
	}
	if errRead == nil {
		if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
			return state, errUnmarshal
		}
	}
	if state.Months == nil {
		state.Months = make(map[string]map[string]*Row)
	}
	if state.Days == nil {
		state.Days = make(map[string]*Totals)
	}
	return state, nil
}

func mergeDays(dst, src map[string]*Totals) {
	for day, totals := range src {
		if dst[day] == nil {
			dst[day] = &Totals{Period: day}
		}
		dst[day].add(totals.Requests, totals.TotalTokens, totals.CostUSD, totals.UnpricedTokens)
	}
}

func mergeMonths(dst, src map[string]map[string]*Row) {
	for month, rows := range src {
		if dst[month] == nil {
			dst[month] = make(map[string]*Row, len(rows))
		}
		for key, row := range rows {
			existing := dst[month][key]
			if existing == nil {
				dst[month][key] = row
				continue
			}
			existing.Requests += row.Requests
			existing.Failed += row.Failed
			existing.InputTokens += row.InputTokens
			existing.CacheReadTokens += row.CacheReadTokens
			existing.CacheWriteTokens += row.CacheWriteTokens
			existing.OutputTokens += row.OutputTokens
			existing.ReasoningTokens += row.ReasoningTokens
			existing.TotalTokens += row.TotalTokens
			existing.CostUSD += row.CostUSD
			existing.UnpricedTokens += row.UnpricedTokens
		}
	}
}

func maskKey(apiKey string) string {
	if len(apiKey) <= 8 {
		return "key-" + KeyID(apiKey)[:8]
	}
	return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
}
