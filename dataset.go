package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const globalURL = "https://raw.githubusercontent.com/lonetis/crux-cache/main/data/global/"

type snapshot struct {
	Month   string
	Origins int
	Bytes   int64
}

type chunk struct {
	Filename string
	Size     int64
	Origins  int
}

type release struct {
	Origins int
	Chunks  []chunk
}

func latestDataset(root string) (string, snapshot, error) {
	dirs, err := filepath.Glob(filepath.Join(root, "global-[0-9][0-9][0-9][0-9][0-9][0-9]"))
	if err != nil {
		return "", snapshot{}, err
	}
	if len(dirs) == 0 {
		return "", snapshot{}, errors.New("download the global dataset with webster fetch")
	}
	return openDataset(dirs[len(dirs)-1])
}

func openDataset(dir string) (string, snapshot, error) {
	var meta snapshot
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return "", meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", meta, err
	}
	path := filepath.Join(dir, "origins.csv")
	info, err := os.Stat(path)
	if err != nil {
		return "", meta, err
	}
	if info.Size() != meta.Bytes || meta.Origins < 1 {
		return "", meta, errors.New("incomplete CrUX dataset in " + dir)
	}
	return path, meta, nil
}

func fetchDataset(ctx context.Context, root string, log io.Writer) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	get := func(name string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, globalURL+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s", name, resp.Status)
		}
		return resp.Body, nil
	}
	body, err := get("manifest.json")
	if err != nil {
		return err
	}
	var manifest struct {
		Summary struct {
			Latest string `json:"latest_month"`
		}
		Months map[string]release
	}
	err = json.NewDecoder(body).Decode(&manifest)
	body.Close()
	if err != nil {
		return err
	}
	month := manifest.Summary.Latest
	rel := manifest.Months[month]
	if _, err := time.Parse("200601", month); err != nil || len(rel.Chunks) == 0 || rel.Origins < 1 {
		return errors.New("invalid global CrUX manifest")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	dir := filepath.Join(root, "global-"+month)
	if path, meta, err := openDataset(dir); err == nil && meta.Origins == rel.Origins {
		fmt.Fprintf(log, "Already downloaded %d origins in %s\n", meta.Origins, path)
		return nil
	}
	tmp, err := os.MkdirTemp(root, ".download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	out, err := os.Create(filepath.Join(tmp, "origins.csv"))
	if err != nil {
		return err
	}
	defer out.Close()
	writer := bufio.NewWriterSize(out, 128*1024)
	if _, err := writer.WriteString("origin,rank\n"); err != nil {
		return err
	}
	count := 0
	for i, part := range rel.Chunks {
		fmt.Fprintf(log, "[%d/%d] %s\n", i+1, len(rel.Chunks), part.Filename)
		body, err := get(part.Filename)
		if err != nil {
			return err
		}
		n, err := copyChunk(writer, body, part)
		body.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", part.Filename, err)
		}
		count += n
	}
	if count != rel.Origins {
		return fmt.Errorf("expected %d origins, received %d", rel.Origins, count)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	info, err := out.Stat()
	if err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(snapshot{month, count, info.Size()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "snapshot.json"), meta, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	fmt.Fprintf(log, "Downloaded %d origins to %s\n", count, filepath.Join(dir, "origins.csv"))
	return nil
}

func copyChunk(out *bufio.Writer, body io.Reader, part chunk) (int, error) {
	reader := bufio.NewReaderSize(body, 64*1024)
	var size int64
	rows := 0
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil && err != io.EOF {
			return rows, err
		}
		size += int64(len(line))
		if len(line) > 0 && !bytes.Equal(bytes.TrimSpace(line), []byte("origin,rank")) {
			if _, e := out.Write(line); e != nil {
				return rows, e
			}
			if line[len(line)-1] != '\n' {
				if e := out.WriteByte('\n'); e != nil {
					return rows, e
				}
			}
			rows++
		}
		if err == io.EOF {
			break
		}
	}
	if size != part.Size || rows != part.Origins {
		return rows, fmt.Errorf("received %d bytes and %d rows, expected %d and %d", size, rows, part.Size, part.Origins)
	}
	return rows, nil
}
