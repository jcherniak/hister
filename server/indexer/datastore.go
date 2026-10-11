// SPDX-License-Identifier: AGPL-3.0-or-later

package indexer

import (
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asciimoo/hister/server/visual"

	"github.com/rs/zerolog/log"
)

const (
	dataDirName   = "data"
	htmlSubdir    = "html"
	faviconSubdir = "favicon"
)

// dataStore manages the content-addressed file-system store for HTML and
// favicon data. A fixed pool of 256 per-shard RWMutexes (indexed by the first
// byte of the SHA-256 key) serialises concurrent operations on files in the
// same shard while allowing fully parallel access across different shards.
//
// The struct is shared between the live indexer and the temporary indexer
// created during reindex so that both always use the same locks.
type dataStore struct {
	dir    string
	shards [256]sync.RWMutex
}

func newDataStore(dir string) *dataStore {
	return &dataStore{dir: dir}
}

// shard returns the mutex responsible for key, using the first byte of the
// hex-encoded SHA-256 string as the shard index (256 shards total).
func (ds *dataStore) shard(key string) *sync.RWMutex {
	if len(key) < 2 {
		return &ds.shards[0]
	}
	hi := hexNibble(key[0])
	lo := hexNibble(key[1])
	return &ds.shards[hi<<4|lo]
}

// hexNibble converts a lowercase hex character to its numeric value.
func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}

// write compresses data and writes it to the content-addressed store under
// subdir. The SHA-256 hash is computed first (no lock needed), then the
// key's shard is exclusively locked for the stat+create to prevent races
// between a concurrent delete of the same key.
func (ds *dataStore) write(subdir string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	sum := sha256.Sum256(data)
	key := fmt.Sprintf("%x", sum)
	mu := ds.shard(key)
	mu.Lock()
	defer mu.Unlock()
	if err := writeFileLocked(ds.dir, subdir, key, data); err != nil {
		return "", err
	}
	return key, nil
}

// read decompresses and returns the data file identified by key. Acquires a
// shared lock on the key's shard, allowing concurrent reads of the same file.
func (ds *dataStore) read(subdir, key string) ([]byte, error) {
	mu := ds.shard(key)
	mu.RLock()
	defer mu.RUnlock()
	return readFileLocked(ds.dir, subdir, key)
}

// deleteIfOrphaned acquires the exclusive shard lock for key, calls refCount
// to check whether any indexed document still references it, and removes the
// file if the count is zero. Holding the lock across both the check and the
// remove makes the operation atomic with respect to concurrent writes or
// deletes of the same key.
func (ds *dataStore) deleteIfOrphaned(field, subdir, key string, refCount func(string, string) uint64) {
	mu := ds.shard(key)
	mu.Lock()
	defer mu.Unlock()
	if refCount(field, key) == 0 {
		for _, path := range []string{dataFilePath(ds.dir, subdir, key), zstdDataFilePath(ds.dir, subdir, key)} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Warn().Err(err).Str("key", key).Str("subdir", subdir).Msg("failed to remove orphaned data file")
			}
		}
	}
}

// cleanup removes unreferenced .gz files under subdir. For each candidate file
// the per-key shard lock is acquired and refCount is called while holding the
// lock, so the check and the removal are atomic with respect to concurrent
// writes or deletes of the same key
func (ds *dataStore) cleanup(field, subdir string, refCount func(string, string) uint64) (int, error) {
	root := filepath.Join(ds.dir, subdir)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return 0, nil
	}
	removed := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Warn().Err(err).Str("path", path).Msg("error accessing data file during cleanup")
			return nil
		}
		ext := filepath.Ext(path)
		if info.IsDir() || (ext != ".gz" && ext != ".zst") {
			return nil
		}
		// Reconstruct the key from the 3-level directory prefix + filename.
		// rel is like "aa/bb/cc/ddeeff....gz"
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := splitPath(rel)
		if len(parts) != 4 {
			return nil
		}
		// parts[3] is the filename with .gz suffix; strip it to get the hash tail.
		key := parts[0] + parts[1] + parts[2] + strings.TrimSuffix(parts[3], ext)
		if !validDataKey(key) {
			return nil
		}
		mu := ds.shard(key)
		mu.Lock()
		if refCount(field, key) == 0 {
			if rerr := os.Remove(path); rerr != nil {
				log.Warn().Err(rerr).Str("path", path).Msg("failed to remove orphaned data file")
			} else {
				removed++
				log.Debug().Str("key", key).Str("subdir", subdir).Msg("removed orphaned data file")
			}
		}
		mu.Unlock()
		return nil
	})
	return removed, err
}

// dataFilePath returns the filesystem path for a stored data file.
// Layout: {dataDir}/{subdir}/{key[0:2]}/{key[2:4]}/{key[4:6]}/{key[6:]}.gz
func dataFilePath(dataDir, subdir, key string) string {
	return filepath.Join(dataDir, subdir, key[0:2], key[2:4], key[4:6], key[6:]+".gz")
}

func zstdDataFilePath(dataDir, subdir, key string) string {
	return strings.TrimSuffix(dataFilePath(dataDir, subdir, key), ".gz") + ".zst"
}

// writeFileLocked writes data compressed with gzip to the path derived from
// key. If the file already exists it is left unchanged (same hash = same
// content). The caller must hold the relevant shard lock.
func writeFileLocked(dataDir, subdir, key string, data []byte) error {
	fpath := dataFilePath(dataDir, subdir, key)
	if _, err := os.Stat(fpath); err == nil {
		return nil
	}
	if subdir == htmlSubdir {
		fpath = zstdDataFilePath(dataDir, subdir, key)
		if _, err := os.Stat(fpath); err == nil {
			return nil
		}
		compressed, err := visual.CompressText(data)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(fpath), ".pending-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err = file.Write(compressed); err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		return os.Rename(file.Name(), fpath)
	}
	if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	f, err := os.Create(fpath)
	if err != nil {
		return fmt.Errorf("create data file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Warn().Err(cerr).Str("path", fpath).Msg("failed to close data file")
		}
	}()
	w := gzip.NewWriter(f)
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write compressed data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("flush compressed data: %w", err)
	}
	return nil
}

// readFileLocked decompresses and returns the data file identified by key.
// The caller must hold the relevant shard lock.
func readFileLocked(dataDir, subdir, key string) ([]byte, error) {
	if !validDataKey(key) {
		return nil, fmt.Errorf("invalid data key")
	}
	if data, err := os.ReadFile(zstdDataFilePath(dataDir, subdir, key)); err == nil {
		return visual.DecompressText(data, visual.MaxStoredTextBytes)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	fpath := dataFilePath(dataDir, subdir, key)
	f, err := os.Open(fpath)
	if err != nil {
		return nil, fmt.Errorf("open data file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Warn().Err(cerr).Str("path", fpath).Msg("failed to close data file")
		}
	}()
	r, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			log.Warn().Err(cerr).Str("path", fpath).Msg("failed to close gzip reader")
		}
	}()
	data, err := io.ReadAll(io.LimitReader(r, visual.MaxStoredTextBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompress data: %w", err)
	}
	if len(data) > visual.MaxStoredTextBytes {
		return nil, fmt.Errorf("stored data exceeds size limit")
	}
	return data, nil
}

// splitPath splits a filepath into its individual components.
func splitPath(p string) []string {
	var parts []string
	for {
		dir, file := filepath.Split(filepath.Clean(p))
		if file == "" || file == "." {
			break
		}
		parts = append([]string{file}, parts...)
		p = dir
		if dir == "" || dir == "." {
			break
		}
	}
	return parts
}
