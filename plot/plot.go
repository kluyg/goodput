// Command plot renders the simulation CSVs into self-contained SVG line charts.
// No third-party dependencies — just enough SVG to tell the story.
//
//	go run ./plot
//
// Reads data_fifo.csv, data_lifo.csv, data_deadline.csv from the working dir and
// writes chart_*.svg.
package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type row struct {
	t          float64
	offered    float64
	throughput float64
	goodput    float64
	dropped    float64
	queue      float64
}

func load(path string) ([]row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []row
	for i, rec := range recs {
		if i == 0 || len(rec) < 6 {
			continue // header
		}
		n := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		out = append(out, row{n(rec[0]), n(rec[1]), n(rec[2]), n(rec[3]), n(rec[4]), n(rec[5])})
	}
	return out, nil
}

const (
	w, h               = 760, 320
	padL, padR         = 56, 16
	padT, padB         = 28, 36
	spikeStart, spikeE = 20.0, 30.0
)

type series struct {
	label string
	color string
	vals  func(row) float64
}

func maxOf(rows []row, sels []series) (maxX, maxY float64) {
	for _, r := range rows {
		if r.t > maxX {
			maxX = r.t
		}
		for _, s := range sels {
			if v := s.vals(r); v > maxY {
				maxY = v
			}
		}
	}
	if maxY == 0 {
		maxY = 1
	}
	return
}

// niceCeil rounds up to a readable axis maximum.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	pow := 1.0
	for v/pow > 10 {
		pow *= 10
	}
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*pow {
			return m * pow
		}
	}
	return 10 * pow
}

func chart(title string, rows []row, sels []series, fname string) error {
	maxX, rawMaxY := maxOf(rows, sels)
	maxY := niceCeil(rawMaxY)
	X := func(t float64) float64 { return padL + (t/maxX)*(w-padL-padR) }
	Y := func(v float64) float64 { return h - padB - (v/maxY)*(h-padT-padB) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="ui-sans-serif,system-ui,sans-serif">`, w, h)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="white"/>`, w, h)
	fmt.Fprintf(&b, `<text x="%d" y="18" font-size="14" font-weight="600" fill="#111">%s</text>`, padL, title)

	// Spike shading.
	fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="#f0f0f0"/>`,
		X(spikeStart), padT, X(spikeE)-X(spikeStart), h-padT-padB)
	fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#999">spike</text>`, X(spikeStart)+3, padT+12)

	// Y gridlines + labels (5 steps).
	for i := 0; i <= 5; i++ {
		v := maxY * float64(i) / 5
		y := Y(v)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#eee"/>`, padL, y, w-padR, y)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#888" text-anchor="end">%s</text>`,
			padL-6, y+3, trim(v))
	}
	// X axis labels.
	for t := 0.0; t <= maxX; t += 20 {
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#888" text-anchor="middle">%.0fs</text>`, X(t), h-padB+16, t)
	}

	// Series polylines.
	for _, s := range sels {
		var pts strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&pts, "%.1f,%.1f ", X(r.t), Y(s.vals(r)))
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" points="%s"/>`, s.color, strings.TrimSpace(pts.String()))
	}

	// Legend.
	lx := w - padR - 150
	for i, s := range sels {
		ly := padT + 6 + float64(i)*16
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="12" height="3" fill="%s"/>`, float64(lx), ly, s.color)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="11" fill="#333">%s</text>`, float64(lx)+18, ly+4, s.label)
	}

	b.WriteString(`</svg>`)
	return os.WriteFile(fname, []byte(b.String()), 0644)
}

func trim(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.0fk", v/1000)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// order drives both file iteration and the comparison-chart legend order.
var order = []string{"fifo", "lifo", "deadline_naive", "deadline"}

var titles = map[string]string{
	"fifo":           "FIFO — throughput flat, goodput collapses",
	"lifo":           "LIFO — newest first, goodput survives (queue still grows)",
	"deadline_naive": "Deadline drop, margin 0 — queue bounded, goodput still 0",
	"deadline":       "Deadline drop, backend deadline < client — bounded AND good",
}

var palette = map[string]string{
	"fifo":           "#d62728",
	"lifo":           "#1f77b4",
	"deadline_naive": "#ff7f0e",
	"deadline":       "#2ca02c",
}

func main() {
	loaded := map[string][]row{}
	for _, d := range order {
		rows, err := load("data_" + d + ".csv")
		if err != nil {
			fmt.Fprintln(os.Stderr, "skip", d, err)
			continue
		}
		loaded[d] = rows

		// Hero chart: throughput (flat) vs goodput (the casualty), scaled to the
		// capacity range so both lines are actually visible.
		hero := []series{
			{"throughput", "#1f77b4", func(r row) float64 { return r.throughput }},
			{"goodput", "#2ca02c", func(r row) float64 { return r.goodput }},
		}
		chart(titles[d], rows, hero, "chart_"+d+".svg")

		// Offered load — shows the retry storm amplifying well past the spike rate.
		off := []series{{"offered (incl. retries)", "#9467bd", func(r row) float64 { return r.offered }}}
		chart(titles[d]+" — offered load", rows, off, "chart_"+d+"_offered.svg")

		// Queue depth.
		qd := []series{{"queue depth", "#d62728", func(r row) float64 { return r.queue }}}
		chart(titles[d]+" — queue depth", rows, qd, "chart_"+d+"_queue.svg")

		fmt.Println("wrote chart_" + d + ".svg")
	}

	// THE summary chart: goodput for every discipline on one capacity-scaled axis.
	compare("Goodput by queue discipline", loaded, "chart_goodput_compare.svg",
		func(r row) float64 { return r.goodput })
	// Queue depth comparison — deadline disciplines hug the floor while FIFO/LIFO explode.
	compare("Queue depth by queue discipline", loaded, "chart_queue_compare.svg",
		func(r row) float64 { return r.queue })
	fmt.Println("wrote chart_goodput_compare.svg, chart_queue_compare.svg")
}

// compare overlays one metric from every discipline on a single chart.
func compare(title string, loaded map[string][]row, fname string, sel func(row) float64) {
	var maxX, maxY float64
	for _, rows := range loaded {
		for _, r := range rows {
			if r.t > maxX {
				maxX = r.t
			}
			if v := sel(r); v > maxY {
				maxY = v
			}
		}
	}
	maxY = niceCeil(maxY)
	if maxX == 0 {
		maxX = 1
	}
	X := func(t float64) float64 { return padL + (t/maxX)*(w-padL-padR) }
	Y := func(v float64) float64 { return h - padB - (v/maxY)*(h-padT-padB) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="ui-sans-serif,system-ui,sans-serif">`, w, h)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="white"/>`, w, h)
	fmt.Fprintf(&b, `<text x="%d" y="18" font-size="14" font-weight="600" fill="#111">%s</text>`, padL, title)
	fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="#f0f0f0"/>`,
		X(spikeStart), padT, X(spikeE)-X(spikeStart), h-padT-padB)
	fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#999">spike</text>`, X(spikeStart)+3, padT+12)
	for i := 0; i <= 5; i++ {
		v := maxY * float64(i) / 5
		y := Y(v)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#eee"/>`, padL, y, w-padR, y)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#888" text-anchor="end">%s</text>`, padL-6, y+3, trim(v))
	}
	for t := 0.0; t <= maxX; t += 20 {
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#888" text-anchor="middle">%.0fs</text>`, X(t), h-padB+16, t)
	}
	for i, d := range order {
		rows, ok := loaded[d]
		if !ok {
			continue
		}
		var pts strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&pts, "%.1f,%.1f ", X(r.t), Y(sel(r)))
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" points="%s"/>`, palette[d], strings.TrimSpace(pts.String()))
		ly := padT + 6 + float64(i)*16
		lx := w - padR - 150
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="12" height="3" fill="%s"/>`, float64(lx), ly, palette[d])
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="11" fill="#333">%s</text>`, float64(lx)+18, ly+4, d)
	}
	b.WriteString(`</svg>`)
	if err := os.WriteFile(fname, []byte(b.String()), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
