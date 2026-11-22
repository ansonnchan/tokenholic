package ccusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// Counts are the four token categories a ccusage row reports. ccusage writes
// zero for a category an agent's records do not carry, so a zero here is not
// yet a known zero: the caller decides that from what the agent can report.
type Counts struct {
	Input         uint64
	Output        uint64
	CacheCreation uint64
	CacheRead     uint64
}

// ModelUsage is one model's share of a day, under the name ccusage gave it.
type ModelUsage struct {
	Model string
	Counts
}

// Day is one validated row of a daily report: one UTC date, its totals, and
// the per-model breakdown.
type Day struct {
	// Date is the UTC calendar date, YYYY-MM-DD.
	Date string
	// Counts are the day's totals across every model.
	Counts
	// Total is ccusage's totalTokens. It can exceed the four categories.
	Total  uint64
	Models []ModelUsage
}

// Extra returns the tokens ccusage counts in the day's total outside the
// four categories. For the agents in this contract those are reasoning
// tokens the agent reports apart from its output. The daily report gives
// them per day only, never per model.
func (d Day) Extra() uint64 {
	return d.Total - d.Input - d.Output - d.CacheCreation - d.CacheRead
}

// Unattributed returns the part of the day's categories that no model
// breakdown accounts for.
func (d Day) Unattributed() Counts {
	rest := d.Counts
	for _, model := range d.Models {
		rest.Input -= model.Input
		rest.Output -= model.Output
		rest.CacheCreation -= model.CacheCreation
		rest.CacheRead -= model.CacheRead
	}
	return rest
}

// Report is a validated daily report for one agent.
type Report struct {
	Days []Day
}

// maxReportDays bounds a report at well over a human lifetime of daily rows.
const maxReportDays = 36600

// The wire types name every field the pinned release can emit. Cost and
// pricing fields are declared so a report that carries them still decodes,
// and are never read: tokenholic prices usage itself.
type wireCounts struct {
	InputTokens         *uint64 `json:"inputTokens"`
	OutputTokens        *uint64 `json:"outputTokens"`
	CacheCreationTokens *uint64 `json:"cacheCreationTokens"`
	CacheReadTokens     *uint64 `json:"cacheReadTokens"`
}

type wireModel struct {
	wireCounts
	ModelName             *string         `json:"modelName"`
	CacheCreation1hTokens *uint64         `json:"cacheCreation1hTokens"`
	MissingPricing        *bool           `json:"missingPricing"`
	Cost                  json.RawMessage `json:"cost"`
	StoredCost            json.RawMessage `json:"storedCost"`
}

type wireDay struct {
	wireCounts
	Date            *string         `json:"date"`
	TotalTokens     *uint64         `json:"totalTokens"`
	ModelsUsed      []string        `json:"modelsUsed"`
	ModelBreakdowns []wireModel     `json:"modelBreakdowns"`
	MessageCount    *uint64         `json:"messageCount"`
	Credits         json.RawMessage `json:"credits"`
	TotalCost       json.RawMessage `json:"totalCost"`
}

type wireTotals struct {
	wireCounts
	TotalTokens    *uint64         `json:"totalTokens"`
	UnpricedModels []string        `json:"unpricedModels"`
	Credits        json.RawMessage `json:"credits"`
	TotalCost      json.RawMessage `json:"totalCost"`
}

type wireReport struct {
	Daily  []wireDay       `json:"daily"`
	Totals json.RawMessage `json:"totals"`
}

// ParseDailyReport decodes and validates the output of one pinned daily
// report. It rejects anything the contract does not describe: unknown or
// duplicate fields, missing or non-integer counts, an invalid or repeated
// date, a breakdown that exceeds its day, a total smaller than its parts,
// and report totals that differ from the rows. A report that fails is
// rejected whole; no row of it is used.
func ParseDailyReport(data []byte) (Report, error) {
	var wire wireReport
	if err := strictDecode(data, &wire); err != nil {
		return Report{}, err
	}
	if wire.Daily == nil || wire.Totals == nil {
		return Report{}, errors.New("daily and totals are required")
	}
	if len(wire.Daily) > maxReportDays {
		return Report{}, errors.New("report has too many daily rows")
	}
	report := Report{Days: make([]Day, 0, len(wire.Daily))}
	seen := make(map[string]struct{}, len(wire.Daily))
	var sum Counts
	var sumTotal uint64
	for _, row := range wire.Daily {
		day, err := row.validate()
		if err != nil {
			return Report{}, err
		}
		if _, duplicate := seen[day.Date]; duplicate {
			return Report{}, fmt.Errorf("daily row %s is repeated", day.Date)
		}
		seen[day.Date] = struct{}{}
		if err := sum.add(day.Counts); err != nil {
			return Report{}, err
		}
		if sumTotal, err = addChecked(sumTotal, day.Total); err != nil {
			return Report{}, err
		}
		report.Days = append(report.Days, day)
	}
	if string(bytes.TrimSpace(wire.Totals)) == "null" {
		// One agent's report writes null totals instead of zeroes when it
		// has no rows. With rows, totals are always an object.
		if len(report.Days) != 0 {
			return Report{}, errors.New("totals are null in a report that has rows")
		}
		return report, nil
	}
	var totals wireTotals
	if err := strictDecode(wire.Totals, &totals); err != nil {
		return Report{}, fmt.Errorf("totals: %w", err)
	}
	declared, err := totals.wireCounts.values()
	if err != nil {
		return Report{}, fmt.Errorf("totals: %w", err)
	}
	if totals.TotalTokens == nil {
		return Report{}, errors.New("totals: totalTokens is missing")
	}
	if declared != sum || *totals.TotalTokens != sumTotal {
		return Report{}, errors.New("totals do not match the daily rows")
	}
	return report, nil
}

func (row wireDay) validate() (Day, error) {
	if row.Date == nil {
		return Day{}, errors.New("daily row has no date")
	}
	parsed, err := time.Parse("2006-01-02", *row.Date)
	if err != nil || parsed.Format("2006-01-02") != *row.Date {
		return Day{}, errors.New("daily row has an invalid date")
	}
	counts, err := row.wireCounts.values()
	if err != nil {
		return Day{}, fmt.Errorf("daily row %s: %w", *row.Date, err)
	}
	if row.TotalTokens == nil {
		return Day{}, fmt.Errorf("daily row %s: totalTokens is missing", *row.Date)
	}
	parts, err := counts.total()
	if err != nil {
		return Day{}, fmt.Errorf("daily row %s: %w", *row.Date, err)
	}
	if *row.TotalTokens < parts {
		return Day{}, fmt.Errorf("daily row %s: totalTokens is smaller than its categories", *row.Date)
	}
	if row.ModelsUsed == nil || row.ModelBreakdowns == nil {
		return Day{}, fmt.Errorf("daily row %s: modelsUsed and modelBreakdowns are required", *row.Date)
	}
	day := Day{Date: *row.Date, Counts: counts, Total: *row.TotalTokens, Models: make([]ModelUsage, 0, len(row.ModelBreakdowns))}
	named := make(map[string]struct{}, len(row.ModelBreakdowns))
	var attributed Counts
	for _, breakdown := range row.ModelBreakdowns {
		if breakdown.ModelName == nil || *breakdown.ModelName == "" || len(*breakdown.ModelName) > 256 {
			return Day{}, fmt.Errorf("daily row %s: a model breakdown has no usable name", day.Date)
		}
		if _, duplicate := named[*breakdown.ModelName]; duplicate {
			return Day{}, fmt.Errorf("daily row %s: a model breakdown is repeated", day.Date)
		}
		named[*breakdown.ModelName] = struct{}{}
		usage, err := breakdown.wireCounts.values()
		if err != nil {
			return Day{}, fmt.Errorf("daily row %s: model breakdown: %w", day.Date, err)
		}
		if breakdown.CacheCreation1hTokens != nil && *breakdown.CacheCreation1hTokens > usage.CacheCreation {
			return Day{}, fmt.Errorf("daily row %s: a 1-hour cache write exceeds its total", day.Date)
		}
		if err := attributed.add(usage); err != nil {
			return Day{}, fmt.Errorf("daily row %s: %w", day.Date, err)
		}
		day.Models = append(day.Models, ModelUsage{Model: *breakdown.ModelName, Counts: usage})
	}
	if attributed.Input > counts.Input || attributed.Output > counts.Output ||
		attributed.CacheCreation > counts.CacheCreation || attributed.CacheRead > counts.CacheRead {
		return Day{}, fmt.Errorf("daily row %s: model breakdowns exceed the day's totals", day.Date)
	}
	if len(row.ModelsUsed) != len(named) {
		return Day{}, fmt.Errorf("daily row %s: modelsUsed does not match the model breakdowns", day.Date)
	}
	listed := make(map[string]struct{}, len(row.ModelsUsed))
	for _, model := range row.ModelsUsed {
		if _, found := named[model]; !found {
			return Day{}, fmt.Errorf("daily row %s: modelsUsed does not match the model breakdowns", day.Date)
		}
		listed[model] = struct{}{}
	}
	if len(listed) != len(named) {
		return Day{}, fmt.Errorf("daily row %s: modelsUsed repeats a model", day.Date)
	}
	return day, nil
}

func (c wireCounts) values() (Counts, error) {
	if c.InputTokens == nil || c.OutputTokens == nil || c.CacheCreationTokens == nil || c.CacheReadTokens == nil {
		return Counts{}, errors.New("a token count is missing or null")
	}
	return Counts{
		Input: *c.InputTokens, Output: *c.OutputTokens,
		CacheCreation: *c.CacheCreationTokens, CacheRead: *c.CacheReadTokens,
	}, nil
}

func (c Counts) total() (uint64, error) {
	var total uint64
	var err error
	for _, value := range []uint64{c.Input, c.Output, c.CacheCreation, c.CacheRead} {
		if total, err = addChecked(total, value); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (c *Counts) add(other Counts) error {
	var err error
	if c.Input, err = addChecked(c.Input, other.Input); err != nil {
		return err
	}
	if c.Output, err = addChecked(c.Output, other.Output); err != nil {
		return err
	}
	if c.CacheCreation, err = addChecked(c.CacheCreation, other.CacheCreation); err != nil {
		return err
	}
	c.CacheRead, err = addChecked(c.CacheRead, other.CacheRead)
	return err
}

func addChecked(left, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, errors.New("a token count overflows")
	}
	return left + right, nil
}

func strictDecode(data []byte, target any) error {
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}
