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
	maxLines := flag.Int("max-lines", 100000, "stop reading after this many lines (0 = unlimited)")
	verbose := flag.Bool("verbose", false, "log diagnostics to stderr")
	quiet := flag.Bool("quiet", false, "suppress trailing comment line on stdout")
	configDir := flag.String("config-dir", "", "path to externalized pattern library (default: ./.log2grok)")
	resetConfig := flag.Bool("reset-config", false, "back up and overwrite the externalized library with embedded defaults, then exit")
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
