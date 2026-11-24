package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

func printScan(ctx context.Context, out, log io.Writer, input string, meta snapshot, config settings) error {
	s, err := startScan(ctx, input, config, meta.Origins)
	if err != nil {
		return err
	}
	defer s.finish()
	fmt.Fprintf(log, "%s · Targets %s · Workers %d · Timeout %ds · DNS %s\n",
		config.checkLabel(), targetLabel(s.goal), config.processes, config.timeout, config.dnsLabel())

	// Match the dashboard's gradual worker startup and keep reading reports between batches.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	nextProgress := s.started.Add(5 * time.Second)
	s.fill()
	for !s.done() && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case report := <-s.reports:
			s.accept(report)
		case now := <-ticker.C:
			s.fill()
			if !now.Before(nextProgress) {
				fmt.Fprintf(log, "%d completed · %s\n", s.completed, s.outcomeTotals())
				nextProgress = now.Add(5 * time.Second)
			}
		}
	}
	s.finish()
	var summary strings.Builder
	if s.err != nil || ctx.Err() != nil {
		summary.WriteString("Partial results · ")
	}
	fmt.Fprintf(&summary, "%d completed · %s\n", s.completed, s.outcomeTotals())
	if len(s.statuses) > 0 {
		summary.WriteString("HTTP status")
		for _, code := range slices.Sorted(maps.Keys(s.statuses)) {
			fmt.Fprintf(&summary, "  %d=%d", code, s.statuses[code])
		}
		summary.WriteByte('\n')
	}
	if len(s.reasons) > 0 {
		summary.WriteString("Failure reasons")
		for _, reason := range slices.Sorted(maps.Keys(s.reasons)) {
			fmt.Fprintf(&summary, "  %s=%d", reason, s.reasons[reason])
		}
		summary.WriteByte('\n')
	}
	for _, endpoint := range slices.Sorted(maps.Keys(s.resolverStats)) {
		fmt.Fprintf(&summary, "DNS %s  %s\n", endpointLabel(endpoint), strings.Join(s.resolverStats[endpoint].counters(), "  "))
	}
	fmt.Fprintf(&summary, "Elapsed %s · %.1f origins/s\n", s.elapsed().Round(time.Millisecond), float64(s.completed)/max(.001, s.elapsed().Seconds()))
	if _, err := io.WriteString(out, summary.String()); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.err
}
