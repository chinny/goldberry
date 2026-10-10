package web

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/service"
	"github.com/chinny/goldberry/internal/store"
)

// A balance graph is SVG drawn on the server, so it works without JS and
// needs no inline styles. The plot stretches to its box
// (preserveAspectRatio="none", strokes don't scale), and the axis labels are
// HTML beside it: the y labels sit in equal rows and the x labels in equal
// columns, so the ticks are placed at row and column centres to line up.
const (
	chartW     = 1000.0
	chartH     = 300.0
	xColumns   = 4
	maxDots    = 120 // past this a line is too busy for dots; the list still has every change
	yIntervals = 3
)

type chart struct {
	Lines   []chartLine
	YTicks  []string // top to bottom
	XTicks  []string // left to right
	Grid    []string // y of each y tick, in plot units
	Compact bool     // a preview: no axes, dots or legend
	Label   string   // for screen readers
	Legend  bool     // two or more lines
}

type chartLine struct {
	Name  string
	Class string // total | spend | save | give | custom
	Path  string
	Area  string // filled wash under a lone line
	End   int64
	Delta int64 // End − Start
	Dots  []chartDot
}

type chartDot struct {
	D     string // a zero-length path: round caps draw the dot at any stretch
	Href  string
	Title string
}

// buildChart lays out a balance history for the "chart" template.
func buildChart(h service.BalanceHistory, cur money.Currency, loc *time.Location, compact bool) chart {
	c := chart{Compact: compact, Legend: len(h.Series) > 1 && !compact}
	lo, hi := int64(0), int64(0) // the baseline is always zero
	for _, s := range h.Series {
		lo, hi = min(lo, s.Low), max(hi, s.High)
	}
	step := niceStep(hi-lo, yIntervals, cur)
	tlo, thi := floorTo(lo, step), ceilTo(hi, step)
	if thi == tlo {
		thi += step
	}
	// Half a step of padding each side puts every tick at a row centre.
	dlo, dhi := float64(tlo)-float64(step)/2, float64(thi)+float64(step)/2
	y := func(v int64) float64 { return chartH - (float64(v)-dlo)/(dhi-dlo)*chartH }
	for t := thi; t >= tlo; t -= step {
		c.YTicks = append(c.YTicks, cur.Short(t))
		c.Grid = append(c.Grid, num(y(t)))
	}

	from, to := h.Range.From, h.Range.To
	span := to.Sub(from)
	if span <= 0 {
		span = time.Hour
	}
	x := func(t time.Time) float64 {
		return math.Min(chartW, math.Max(0, float64(t.Sub(from))/float64(span)*chartW))
	}
	layout := "Jan 2"
	if span <= 72*time.Hour {
		layout = "Jan 2 3PM"
	}
	for i := range xColumns {
		at := from.Add(time.Duration((float64(i) + 0.5) / xColumns * float64(span)))
		c.XTicks = append(c.XTicks, at.In(loc).Format(layout))
	}

	var label []string
	for _, s := range h.Series {
		ln := chartLine{Name: s.Name, Class: lineClass(s), End: s.End, Delta: s.Change()}
		var p strings.Builder
		p.WriteString("M0 " + num(y(s.Start)))
		for _, pt := range s.Points {
			px, py := num(x(pt.At)), num(y(pt.Balance))
			p.WriteString(" H" + px + " V" + py)
			if !compact && len(s.Points) <= maxDots {
				ln.Dots = append(ln.Dots, chartDot{
					D:     "M" + px + " " + py + "h0",
					Href:  "#e-" + pt.Key,
					Title: pt.At.In(loc).Format("Jan 2") + " · " + cur.Signed(pt.Delta) + " · " + pointText(pt) + " → " + cur.Format(pt.Balance),
				})
			}
		}
		p.WriteString(" H" + num(chartW))
		ln.Path = p.String()
		if len(h.Series) == 1 {
			ln.Area = ln.Path + " V" + num(y(0)) + " H0 Z"
		}
		c.Lines = append(c.Lines, ln)
		label = append(label, s.Name+" went from "+cur.Format(s.Start)+" to "+cur.Format(s.End))
	}
	c.Label = "Balance graph, " + h.Range.FromDate + " to " + h.Range.ToDate + ". " + strings.Join(label, "; ") + "."
	return c
}

func lineClass(s service.HistorySeries) string {
	if s.Kind == "" {
		return "total"
	}
	switch s.Kind {
	case store.JarSpend, store.JarSave, store.JarGive:
		return string(s.Kind)
	}
	return "custom"
}

// pointText is what a kid would call a change: its comment, or its kind.
func pointText(p service.HistoryPoint) string {
	if p.Comment != "" {
		return p.Comment
	}
	switch p.Kind {
	case store.KindDeposit:
		return "Money added"
	case store.KindWithdrawal:
		return "Money out"
	case store.KindAllowance:
		return "Allowance"
	case store.KindInterest:
		return "Interest"
	case store.KindReversal:
		return "Undo"
	case store.KindTransferIn, store.KindTransferOut:
		return "Moved"
	}
	return "Adjustment"
}

// niceStep picks a tick step of 1, 2, 2.5 or 5 × 10ⁿ minor units that splits
// span into about n intervals, never finer than one whole unit of currency.
func niceStep(span int64, n int, cur money.Currency) int64 {
	unit := int64(math.Pow10(cur.Exponent))
	raw := float64(span) / float64(n)
	if raw <= float64(unit) {
		return unit
	}
	mag := math.Pow10(int(math.Floor(math.Log10(raw))))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw {
			step := int64(math.Round(m * mag))
			if step%unit != 0 && step > unit { // 2.5 × a whole unit leaves cents on the axis
				step = (step/unit + 1) * unit
			}
			return step
		}
	}
	return int64(10 * mag)
}

func floorTo(v, step int64) int64 {
	q := v / step
	if v%step != 0 && v < 0 {
		q--
	}
	return q * step
}

func ceilTo(v, step int64) int64 {
	q := v / step
	if v%step != 0 && v > 0 {
		q++
	}
	return q * step
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }
