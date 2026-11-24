package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Read the whole source once. Pack the names into blocks so a full snapshot
// does not leave millions of individually allocated CSV records on the heap.
func loadOrigins(ctx context.Context, path string, knownCount int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReaderSize(f, 256<<10))
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil || len(header) != 2 || header[0] != "origin" || header[1] != "rank" {
		return nil, errors.New("input must be a CrUX CSV with header origin,rank")
	}
	// Treat the manifest count as a hint. A truncated or incorrect manifest
	// should not cause an allocation out of proportion to the input file.
	if stat, err := f.Stat(); err == nil {
		knownCount = int(min(int64(knownCount), stat.Size()/2))
	}
	origins := make([]string, 0, max(0, knownCount))
	var block strings.Builder
	var ends [4096]int
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := 0
		for n < len(ends) {
			row, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("read input: %w", err)
			}
			block.WriteString(row[0])
			ends[n] = block.Len()
			n++
		}
		data, start := block.String(), 0
		for _, end := range ends[:n] {
			origins = append(origins, data[start:end])
			start = end
		}
		if n < len(ends) {
			return origins, nil
		}
		block.Reset()
	}
}
