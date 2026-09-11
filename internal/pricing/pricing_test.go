package pricing

import (
	"strings"
	"testing"
	"time"
)

const datedTable = `{
  "currency": "USD",
  "source": "test",
  "checked": "2026-09-11",
  "rates": [
    {"model": "m", "effective": "2026-03-01", "input": 4, "output": 20, "cache_write_5m": 5, "cache_write_1h": 8, "cache_read": 0.4},
    {"model": "m", "effective": "2026-01-01", "input": 5, "output": 25, "cache_write_5m": 6.25, "cache_write_1h": 10, "cache_read": 0.5}
  ]
}`

func TestLookupPicksTheRateInForce(t *testing.T) {
	tbl, err := Parse([]byte(datedTable))
	if err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time {
		t.Helper()
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// A run between the two effective dates prices at the earlier entry; one
	// on or after the later date at the later entry, regardless of the order
	// the table listed them in.
	if r, ok := tbl.Lookup("m", day("2026-02-15")); !ok || r.Input != 5 {
		t.Fatalf("Lookup(2026-02-15) = %+v, %v, want the 2026-01-01 rate", r, ok)
	}
	if r, ok := tbl.Lookup("m", day("2026-03-01")); !ok || r.Input != 4 {
		t.Fatalf("Lookup(2026-03-01) = %+v, %v, want the 2026-03-01 rate", r, ok)
	}
	if r, ok := tbl.Lookup("m", day("2026-09-11").Add(time.Hour)); !ok || r.Input != 4 {
		t.Fatalf("Lookup(2026-09-11) = %+v, %v, want the 2026-03-01 rate", r, ok)
	}
	// Before every effective date there is no rate: re-running the report
	// over an old window must not reprice history at today's rate.
	if _, ok := tbl.Lookup("m", day("2025-12-31")); ok {
		t.Fatal("Lookup(2025-12-31) found a rate, want none before the first effective date")
	}
	if _, ok := tbl.Lookup("other", day("2026-02-15")); ok {
		t.Fatal("Lookup(other) found a rate, want none for an unknown model")
	}
	if _, ok := tbl.Lookup("m", time.Time{}); ok {
		t.Fatal("Lookup(zero time) found a rate, want none")
	}
}

func TestPriceArithmetic(t *testing.T) {
	r := Rate{Model: "m", Input: 5, Output: 25, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.5}
	// 1,000,000 input = 5; 100,000 output = 2.5; 200,000 5m writes = 1.25;
	// 100,000 1h writes = 1; 2,000,000 cache reads = 1.
	got, err := r.Price(Usage{Input: 1_000_000, Output: 100_000, CacheWrite5m: 200_000, CacheWrite1h: 100_000, CacheRead: 2_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if got != 10.75 {
		t.Fatalf("Price = %v, want 10.75", got)
	}
}

func TestPriceFastMode(t *testing.T) {
	fastIn, fastOut := 10.0, 50.0
	r := Rate{Model: "m", Input: 5, Output: 25, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.5, FastInput: &fastIn, FastOutput: &fastOut}
	// Fast: 1,000,000 input = 10; 100,000 output = 5; 1,000,000 cache reads
	// at 0.1 × the fast input rate = 1.
	got, err := r.Price(Usage{Input: 1_000_000, Output: 100_000, CacheRead: 1_000_000, Fast: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 16 {
		t.Fatalf("fast Price = %v, want 16", got)
	}

	standard := Rate{Model: "m", Input: 5, Output: 25}
	if _, err := standard.Price(Usage{Input: 1, Fast: true}); err == nil || !strings.Contains(err.Error(), `"m"`) {
		t.Fatalf("fast Price on a model without fast rates = %v, want an error naming the model", err)
	}
	unscalable := Rate{Model: "m", Input: 0, Output: 25, CacheRead: 0.5, FastInput: &fastIn, FastOutput: &fastOut}
	if _, err := unscalable.Price(Usage{Input: 1, Fast: true}); err == nil || !strings.Contains(err.Error(), `"m"`) {
		t.Fatalf("fast Price on a model without a standard input rate = %v, want an error naming the model", err)
	}
}

// TestPriceFastModeUsesTheEntrysOwnCacheRatios pins that fast cache rates
// scale the entry's own cache rates rather than assuming the usual
// multipliers: a model whose cache hit is 0.025× input prices fast cache
// reads at 0.025× the fast input rate.
func TestPriceFastModeUsesTheEntrysOwnCacheRatios(t *testing.T) {
	fastIn, fastOut := 20.0, 100.0
	r := Rate{Model: "m", Input: 10, Output: 50, CacheWrite5m: 12.5, CacheWrite1h: 20, CacheRead: 0.25, FastInput: &fastIn, FastOutput: &fastOut}
	// 1,000,000 fast cache reads at 0.025 × $20/M = 0.5.
	got, err := r.Price(Usage{CacheRead: 1_000_000, Fast: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.5 {
		t.Fatalf("fast cache-read Price = %v, want 0.5", got)
	}
}

func TestParseRejectsAMalformedTable(t *testing.T) {
	cases := map[string]string{
		"not json": `{`,
		"no checked date": `{"currency":"USD","source":"s","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"no model": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"bad effective": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"Jan 2026","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"negative rate": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":-1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1}]}`,
		"duplicate": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1},
			{"model":"m","effective":"2026-01-01","input":2,"output":2,"cache_write_5m":2,"cache_write_1h":2,"cache_read":2}]}`,
		"fast input alone": `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1,"fast_input":2}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("Parse accepted a malformed table")
			}
		})
	}
}

// TestParseRequiresEveryStandardRate pins that a missing or null rate is an
// error rather than a silent $0: an omitted or misspelled cache_read would
// otherwise zero out the bulk of a run's tokens without warning.
func TestParseRequiresEveryStandardRate(t *testing.T) {
	fields := []string{"input", "output", "cache_write_5m", "cache_write_1h", "cache_read"}
	table := func(omit, null string) string {
		var parts []string
		for _, f := range fields {
			switch f {
			case omit: // dropped from the entry
			case null:
				parts = append(parts, `"`+f+`":null`)
			default:
				parts = append(parts, `"`+f+`":1`)
			}
		}
		// Entry 0 is well-formed so the error must point at entry 1 (n).
		return `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
			{"model":"m","effective":"2026-01-01","input":1,"output":1,"cache_write_5m":1,"cache_write_1h":1,"cache_read":1},
			{"model":"n","effective":"2026-01-01",` + strings.Join(parts, ",") + `}]}`
	}
	for _, f := range fields {
		for _, c := range []struct{ kind, data string }{{"omitted", table(f, "")}, {"null", table("", f)}} {
			t.Run(f+" "+c.kind, func(t *testing.T) {
				_, err := Parse([]byte(c.data))
				if err == nil {
					t.Fatalf("Parse accepted a table with %s %s", f, c.kind)
				}
				msg := err.Error()
				if !strings.Contains(msg, f) || !strings.Contains(msg, "entry 1 (n)") {
					t.Fatalf("error = %q, want it to name entry 1 (n) and field %s", msg, f)
				}
			})
		}
	}

	zero := `{"currency":"USD","source":"s","checked":"2026-09-11","rates":[
		{"model":"free","effective":"2026-01-01","input":0,"output":0,"cache_write_5m":0,"cache_write_1h":0,"cache_read":0}]}`
	tbl, err := Parse([]byte(zero))
	if err != nil {
		t.Fatalf("Parse rejected explicit zero rates: %v", err)
	}
	if r, ok := tbl.Lookup("free", tbl.Checked); !ok || r.Input != 0 || r.CacheRead != 0 {
		t.Fatalf("Lookup(free) = %+v, %v, want an all-zero rate", r, ok)
	}
}

func TestDefaultTableLoadsAndNamesItsProvenance(t *testing.T) {
	tbl, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Currency != "USD" || tbl.Source == "" || tbl.Checked.IsZero() {
		t.Fatalf("table = %+v, want currency, source and checked date", tbl)
	}
	if _, ok := tbl.Lookup("claude-opus-5", tbl.Checked); !ok {
		t.Fatal("claude-opus-5 has no rate on the checked date")
	}
}
