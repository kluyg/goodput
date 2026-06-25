// Command sweep runs the deadline discipline across a range of drop margins and
// reports goodput as a function of the margin. It exists to answer the obvious
// question about `-drop-margin-ms=70`: is the result sensitive to that number?
//
//	go run ./sweep            # default margins
//	go run ./sweep 0 50 70 100 200   # custom margins (ms)
//
// It shells out to the simulation once per margin (real-time runs, so this takes
// a few minutes), parses the summary line, and writes margin_sweep.csv:
//
//	margin_ms,goodput,throughput,goodput_pct,dropped,gaveup
//
// `go run ./plot` then renders chart_margin_sweep.svg from it.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Default margins bracket the 50 ms work time: too small to finish, right at the
// work time, and comfortably above it.
var defaultMargins = []int{0, 30, 50, 55, 60, 70, 100, 150, 250}

const sweepDuration = "60" // seconds per run; shorter than the post's 90 to keep the sweep quick

var summary = regexp.MustCompile(
	`offered=(\d+) throughput=(\d+) goodput=(\d+) dropped=(\d+) userOK=(\d+) gaveUp=(\d+)`)

func main() {
	margins := defaultMargins
	if len(os.Args) > 1 {
		margins = nil
		for _, a := range os.Args[1:] {
			n, err := strconv.Atoi(a)
			if err != nil {
				fmt.Fprintf(os.Stderr, "bad margin %q: %v\n", a, err)
				os.Exit(2)
			}
			margins = append(margins, n)
		}
	}

	out, err := os.Create("margin_sweep.csv")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer out.Close()
	fmt.Fprintln(out, "margin_ms,goodput,throughput,goodput_pct,dropped,gaveup")
	fmt.Printf("%-8s %-9s %-11s %-7s %-9s %-7s\n", "margin", "goodput", "throughput", "good%", "dropped", "gaveup")

	for _, m := range margins {
		cmd := exec.Command("go", "run", ".",
			"-discipline=deadline",
			fmt.Sprintf("-drop-margin-ms=%d", m),
			"-duration="+sweepDuration,
			"-out="+os.DevNull,
		)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "margin=%d failed: %v\n%s\n", m, err, stderr.String())
			continue
		}
		mm := summary.FindStringSubmatch(stderr.String())
		if mm == nil {
			fmt.Fprintf(os.Stderr, "margin=%d: could not parse summary:\n%s\n", m, stderr.String())
			continue
		}
		tput, _ := strconv.Atoi(mm[2])
		good, _ := strconv.Atoi(mm[3])
		dropped, _ := strconv.Atoi(mm[4])
		gaveup, _ := strconv.Atoi(mm[6])
		pct := 0.0
		if tput > 0 {
			pct = 100 * float64(good) / float64(tput)
		}
		fmt.Fprintf(out, "%d,%d,%d,%.1f,%d,%d\n", m, good, tput, pct, dropped, gaveup)
		fmt.Printf("%-8d %-9d %-11d %-7.1f %-9d %-7d\n", m, good, tput, pct, dropped, gaveup)
	}
}
