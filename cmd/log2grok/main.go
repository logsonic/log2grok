package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	l2g "github.com/logsonic/log2grok/pkg/log2grok"
)

func main() {
	threshold := flag.Float64("threshold", 0.85, "library auto-accept threshold (0.0-1.0)")
	maxLines := flag.Int("max-lines", 0, "stop reading after this many lines (0 = unlimited; large inputs are sampled internally, so reading the whole file carries no accuracy penalty)")
	verbose := flag.Bool("verbose", false, "log diagnostics to stderr")
	quiet := flag.Bool("quiet", false, "suppress trailing comment line on stdout")
	configDir := flag.String("config-dir", "", "path to externalized pattern library (default: ./.log2grok)")
	resetConfig := flag.Bool("reset-config", false, "back up and overwrite the externalized library with embedded defaults, then exit")
	multi := flag.Bool("multi", false, "for multi-format logs, emit a set of patterns whose combined coverage reaches --target")
	target := flag.Float64("target", 0.90, "combined-coverage goal for --multi (0.0-1.0)")
	flag.Parse()

	if *resetConfig {
		if err := l2g.ResetConfig(*configDir, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if err := l2g.LoadConfig(*configDir, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: log2grok [flags] <input-file|->")
		os.Exit(1)
	}

	src, err := openInput(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer src.Close()

	lines, truncated, err := readLines(src, *maxLines)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	var diag io.Writer
	if *verbose {
		diag = os.Stderr
	}

	if *multi {
		if err := runMulti(lines, *threshold, *target, *quiet, *verbose, diag); err != nil {
			if errors.Is(err, l2g.ErrEmptyInput) {
				fmt.Fprintln(os.Stderr, "error: input has no non-empty lines")
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		return
	}

	dp, err := discoverLines(lines, truncated, *threshold, *verbose, diag)
	if err != nil {
		if errors.Is(err, l2g.ErrEmptyInput) {
			fmt.Fprintln(os.Stderr, "error: input has no non-empty lines")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	fmt.Println(dp.Grok)
	if !*quiet {
		suffix := ""
		if dp.Truncated {
			suffix = fmt.Sprintf(" (truncated at %d)", *maxLines)
		}
		fmt.Printf("# matched %d / %d lines (%.1f%%) -- %s%s\n",
			dp.MatchedCount, dp.TotalLines, dp.Coverage*100, dp.Source, suffix)
	}
}

// runMulti prints one Grok pattern per line for a multi-format input,
// followed (unless quiet) by a per-pattern coverage comment and a combined
// summary. The bare patterns come first so the output can be piped
// straight into a tool that reads a grok list.
func runMulti(lines []string, threshold, target float64, quiet, verbose bool, diag io.Writer) error {
	res, err := l2g.DiscoverMulti(lines, l2g.Options{
		LibraryThreshold: threshold,
		TargetCoverage:   target,
		Verbose:          verbose,
		Diagnostics:      diag,
	})
	if err != nil {
		return err
	}
	for _, p := range res.Patterns {
		fmt.Println(p.Grok)
	}
	if !quiet {
		for i, p := range res.Patterns {
			fmt.Printf("# [%d] %.1f%% -- %s\n", i+1, p.Coverage*100, p.Source)
		}
		fmt.Printf("# combined %d / %d lines (%.1f%%) across %d patterns\n",
			res.CombinedMatched, res.TotalLines, res.CombinedCoverage*100, len(res.Patterns))
	}
	return nil
}

func discoverLines(lines []string, truncated bool, threshold float64, verbose bool, diag io.Writer) (*l2g.DiscoveredPattern, error) {
	dp, err := l2g.Discover(lines, l2g.Options{
		LibraryThreshold: threshold,
		Verbose:          verbose,
		Diagnostics:      diag,
	})
	if err != nil {
		return nil, err
	}
	if truncated {
		dp.Truncated = true
	}
	return dp, nil
}

func openInput(path string) (io.ReadCloser, error) {
	if path == "-" {
		info, err := os.Stdin.Stat()
		if err != nil {
			return nil, err
		}
		if (info.Mode() & os.ModeCharDevice) != 0 {
			return nil, errors.New("stdin is a TTY; pipe data into log2grok or pass a filename")
		}
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

func readLines(r io.Reader, max int) (lines []string, truncated bool, err error) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	lines = make([]string, 0, 1024)
	for s.Scan() {
		lines = append(lines, s.Text())
		if max > 0 && len(lines) >= max {
			truncated = s.Scan()
			break
		}
	}
	return lines, truncated, s.Err()
}
