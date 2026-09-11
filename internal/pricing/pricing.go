// Package pricing prices token usage from a rate table checked into the repo.
// Transcripts carry tokens, never cost, so every figure downstream comes from
// here; a model or date the table does not cover is a lookup miss the caller
// has to report, never a default rate.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

//go:embed rates.json
var embedded []byte

// perMillion is the denominator every rate in the table is quoted against.
const perMillion = 1e6

// Rate is one model's list prices, in Currency per million tokens, from
// Effective onward. FastInput and FastOutput are nil where the vendor offers
// no fast mode for the model.
type Rate struct {
	Model        string
	Effective    time.Time
	Input        float64
	Output       float64
	CacheWrite5m float64
	CacheWrite1h float64
	CacheRead    float64
	FastInput    *float64
	FastOutput   *float64
}

// Table is the parsed rate table. Source and Checked say where the rates came
// from and when they were last read against it, so a reader can tell a
// current rate from one that has drifted.
type Table struct {
	Currency string
	Source   string
	Checked  time.Time
	// rates holds each model's entries sorted by Effective ascending.
	rates map[string][]Rate
}

// Usage is what one priced unit consumed. CacheWrite5m and CacheWrite1h are
// the two TTL buckets of a cache write. Fast marks fast-mode usage, priced at
// the model's fast rates.
type Usage struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
	Fast         bool
}

var (
	defaultOnce  sync.Once
	defaultTable *Table
	defaultErr   error
)

// Default returns the embedded table, parsed once.
func Default() (*Table, error) {
	defaultOnce.Do(func() {
		defaultTable, defaultErr = Parse(embedded)
	})
	return defaultTable, defaultErr
}

type rateJSON struct {
	Model        string   `json:"model"`
	Effective    string   `json:"effective"`
	Input        *float64 `json:"input"`
	Output       *float64 `json:"output"`
	CacheWrite5m *float64 `json:"cache_write_5m"`
	CacheWrite1h *float64 `json:"cache_write_1h"`
	CacheRead    *float64 `json:"cache_read"`
	FastInput    *float64 `json:"fast_input"`
	FastOutput   *float64 `json:"fast_output"`
}

type tableJSON struct {
	Currency string     `json:"currency"`
	Source   string     `json:"source"`
	Checked  string     `json:"checked"`
	Rates    []rateJSON `json:"rates"`
}

// Parse decodes and validates a rate table. Every entry must carry all five
// standard rates (an explicit 0 is allowed, an omitted or null field is not),
// every rate must be non-negative, every entry must name a model and a
// YYYY-MM-DD effective date, and no two entries may share (model, effective).
func Parse(data []byte) (*Table, error) {
	var raw tableJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse rate table: %w", err)
	}
	if raw.Currency == "" || raw.Source == "" {
		return nil, fmt.Errorf("rate table: currency and source are required")
	}
	checked, err := time.Parse(time.DateOnly, raw.Checked)
	if err != nil {
		return nil, fmt.Errorf("rate table: checked %q: %w", raw.Checked, err)
	}
	t := &Table{Currency: raw.Currency, Source: raw.Source, Checked: checked, rates: map[string][]Rate{}}
	seen := map[string]bool{}
	for i, r := range raw.Rates {
		if r.Model == "" || r.Effective == "" {
			return nil, fmt.Errorf("rate table: entry %d: model and effective are required", i)
		}
		effective, err := time.Parse(time.DateOnly, r.Effective)
		if err != nil {
			return nil, fmt.Errorf("rate table: entry %d (%s): effective %q: %w", i, r.Model, r.Effective, err)
		}
		key := r.Model + "@" + r.Effective
		if seen[key] {
			return nil, fmt.Errorf("rate table: entry %d: duplicate rate for %s effective %s", i, r.Model, r.Effective)
		}
		seen[key] = true
		// Required rates are pointers so an omitted, misspelled or null field is
		// caught rather than silently priced at zero.
		for _, f := range []struct {
			name string
			ptr  *float64
		}{
			{"input", r.Input}, {"output", r.Output}, {"cache_write_5m", r.CacheWrite5m},
			{"cache_write_1h", r.CacheWrite1h}, {"cache_read", r.CacheRead},
		} {
			if f.ptr == nil {
				return nil, fmt.Errorf("rate table: entry %d (%s): %s is required", i, r.Model, f.name)
			}
			if *f.ptr < 0 {
				return nil, fmt.Errorf("rate table: entry %d (%s): %s is negative", i, r.Model, f.name)
			}
		}
		if (r.FastInput == nil) != (r.FastOutput == nil) {
			return nil, fmt.Errorf("rate table: entry %d (%s): fast_input and fast_output must be given together", i, r.Model)
		}
		if r.FastInput != nil && (*r.FastInput < 0 || *r.FastOutput < 0) {
			return nil, fmt.Errorf("rate table: entry %d (%s): fast rate is negative", i, r.Model)
		}
		t.rates[r.Model] = append(t.rates[r.Model], Rate{
			Model:        r.Model,
			Effective:    effective,
			Input:        *r.Input,
			Output:       *r.Output,
			CacheWrite5m: *r.CacheWrite5m,
			CacheWrite1h: *r.CacheWrite1h,
			CacheRead:    *r.CacheRead,
			FastInput:    r.FastInput,
			FastOutput:   r.FastOutput,
		})
	}
	for _, rs := range t.rates {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Effective.Before(rs[j].Effective) })
	}
	return t, nil
}

// Lookup returns the rate in force for model at the given time: the entry with
// the latest Effective not after at. False when the model has no entry, when
// at precedes every entry, or when at is zero — an unknown invocation time
// cannot be priced at any rate.
func (t *Table) Lookup(model string, at time.Time) (Rate, bool) {
	if at.IsZero() {
		return Rate{}, false
	}
	var found Rate
	ok := false
	for _, r := range t.rates[model] {
		if r.Effective.After(at) {
			break
		}
		found, ok = r, true
	}
	return found, ok
}

// Price returns the cost of u at this rate. Fast mode reprices input and
// output; the vendor page states the cache multipliers stack on fast pricing,
// and the multipliers for a model are the ratios its own entry carries
// (Fable 5.1's cache hit is 0.025× input, not the usual 0.1×), so each fast
// cache rate is the entry's standard cache rate scaled by fast_input / input.
// Fast usage on a model without fast rates, or without a standard input rate
// to scale from, is an error rather than a standard-rate estimate.
func (r Rate) Price(u Usage) (float64, error) {
	input, output := r.Input, r.Output
	write5m, write1h, read := r.CacheWrite5m, r.CacheWrite1h, r.CacheRead
	if u.Fast {
		if r.FastInput == nil || r.FastOutput == nil {
			return 0, fmt.Errorf("fast mode has no rate for %q", r.Model)
		}
		if r.Input == 0 {
			return 0, fmt.Errorf("fast mode has no standard input rate to scale cache rates from for %q", r.Model)
		}
		scale := *r.FastInput / r.Input
		input, output = *r.FastInput, *r.FastOutput
		write5m, write1h, read = r.CacheWrite5m*scale, r.CacheWrite1h*scale, r.CacheRead*scale
	}
	cost := float64(u.Input)*input +
		float64(u.Output)*output +
		float64(u.CacheWrite5m)*write5m +
		float64(u.CacheWrite1h)*write1h +
		float64(u.CacheRead)*read
	return cost / perMillion, nil
}
