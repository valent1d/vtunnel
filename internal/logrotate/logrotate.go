// Package logrotate keeps log files from growing without bound on the user's
// machine. It caps files in place by keeping their tail, which is safe to run
// against logs another process appends to (e.g. cloudflared or launchd): at
// worst a few lines are lost at the moment of compaction.
package logrotate

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// CapFile shrinks path to at most maxBytes by keeping the tail, trimmed to start
// on a clean line. A missing file or a file already under the cap is a no-op.
func CapFile(path string, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	tail := make([]byte, maxBytes)
	if _, err := file.ReadAt(tail, info.Size()-maxBytes); err != nil && err != io.EOF {
		return err
	}
	// Drop a leading partial line so the kept content starts on a record boundary.
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	if _, err := file.WriteAt(tail, 0); err != nil {
		return err
	}
	return file.Truncate(int64(len(tail)))
}

// CapDir caps every *.log file directly inside dir. It returns how many files
// it shrank and the first error encountered, continuing past per-file errors.
func CapDir(dir string, maxBytes int64) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	shrunk := 0
	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		if err := CapFile(filepath.Join(dir, entry.Name()), maxBytes); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		shrunk++
	}
	return shrunk, firstErr
}
