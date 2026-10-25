// The subscription metering blocks: Claude plans meter use in 5-hour windows, and this file
// reads a usage row list as those windows — ccusage's live-block view. A block starts at the
// first request past the previous block's end or after a gap of 5 hours or more, and runs
// 5 hours from that request's own UTC hour, so the burn rate and the projection to the window's
// end come straight from the rows. Pure core, like Aggregate: it prices through the Pricer
// it is handed and imports nothing new.
package usage

import (
	"cmp"
	"slices"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// BlockHours is the metering window's span.
const BlockHours = 5

// blockGapMs is the quiet stretch that ends a block: 5 hours without a request.
const blockGapMs = BlockHours * 3_600_000

// blockBurnFloorMs is the shortest span the burn rate divides by, so a block seconds old
// reads as its first minute instead of an unbounded rate.
const blockBurnFloorMs = 60_000

// BlockModels names which requests the block math counts.
type BlockModels int

const (
	// ClaudeModels counts Claude-model requests alone — what a subscription meters.
	ClaudeModels BlockModels = iota
	// AllModels counts every request, the routed provider models included.
	AllModels
)

// Block is one metering window: its ends (the first request's UTC hour, floored, plus five
// hours), the first and last request it holds, the usage that landed in it, the burn rate
// over the minutes it has been active, and the linear projection to its end.
type Block struct {
	Start   int64 `json:"start"`
	End     int64 `json:"end"`
	FirstTS int64 `json:"firstTs"`
	LastTS  int64 `json:"lastTs"`
	Active  bool  `json:"active"`
	Totals
	BurnTokensPerHour float64    `json:"burnTokensPerHour"`
	BurnCostPerHour   shared.USD `json:"burnCostPerHour"`
	ProjectedTokens   float64    `json:"projectedTokens"`
	ProjectedCostUSD  shared.USD `json:"projectedCostUsd"`
}

// BlocksReport is the blocks answer: the windows newest first and the price honesty flags.
type BlocksReport struct {
	Blocks      []Block `json:"blocks"`
	Approximate bool    `json:"approximate"`
	Unpriced    int     `json:"unpriced"`
}

// Blocks folds the rows into metering windows at the clock's own moment: Claude-model
// requests alone unless models says otherwise, a new window at the first request past the
// previous window's end or after a gap of five hours or more, each window five hours from its
// first request's floored UTC hour. The burn rate divides the window's usage by the minutes it
// has been active (capped at its end, floored at a minute) and the projection carries that
// rate to the window's end. A row no price covers counts its tokens at no cost and turns
// Approximate on; a row whose harness reported its cost is priced by that.
func Blocks(rows []Row, now time.Time, models BlockModels, prices Pricer) BlocksReport {
	report := BlocksReport{Blocks: []Block{}}
	kept := make([]Row, 0, len(rows))
	for _, entry := range rows {
		if models == ClaudeModels && cost.CostFamilyOf(entry.Model) != cost.FamilyClaude {
			continue
		}
		kept = append(kept, entry)
	}
	slices.SortStableFunc(kept, func(a, b Row) int { return cmp.Compare(a.TS, b.TS) })
	at := now.UnixMilli()
	var block *Block
	for _, entry := range kept {
		if block != nil && (entry.TS >= block.End || entry.TS-block.LastTS >= blockGapMs) {
			block = nil // the window ran out or a gap ended it; this request opens the next one
		}
		if block == nil {
			hour := time.UnixMilli(entry.TS).UTC().Truncate(time.Hour)
			ends := hour.Add(BlockHours * time.Hour)
			report.Blocks = append(report.Blocks, Block{
				Start:   hour.UnixMilli(),
				End:     ends.UnixMilli(),
				FirstTS: entry.TS,
				LastTS:  entry.TS,
				Active:  at >= hour.UnixMilli() && at < ends.UnixMilli(),
			})
			block = &report.Blocks[len(report.Blocks)-1]
		}
		if entry.TS < block.FirstTS {
			block.FirstTS = entry.TS
		}
		block.LastTS = max(block.LastTS, entry.TS)
		price, priced := priceRow(entry, prices)
		if !priced {
			report.Unpriced++
			report.Approximate = true
			price = RowPrice{}
		}
		addRow(&block.Totals, entry, price)
	}
	for i := range report.Blocks {
		burnOf(&report.Blocks[i], at)
	}
	// newest first: the active window, when there is one, leads
	slices.Reverse(report.Blocks)
	return report
}

// burnOf fills one block's burn rate and projection from the minutes it has been active at
// atMs: the span runs from the block's start to the earlier of the clock and the block's end,
// never shorter than a minute, and the projection carries the rate to the block's end.
func burnOf(block *Block, atMs int64) {
	elapsed := max(min(atMs, block.End)-block.Start, blockBurnFloorMs)
	hours := float64(elapsed) / 3_600_000
	tokens := float64(block.Input + block.Output + block.CacheRead + block.CacheWrite)
	block.BurnTokensPerHour = tokens / hours
	block.BurnCostPerHour = shared.USD(float64(block.CostUSD) / hours)
	if left := max(block.End-atMs, 0); left > 0 {
		block.ProjectedTokens = block.BurnTokensPerHour * (float64(left) / 3_600_000)
		block.ProjectedCostUSD = shared.USD(float64(block.BurnCostPerHour) * (float64(left) / 3_600_000))
	}
}
