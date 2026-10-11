package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asciimoo/hister/server/compression"
)

func zstdDataFilePath(dataDir, subdir, key string) string {
	return strings.TrimSuffix(dataFilePath(dataDir, subdir, key), ".gz") + ".zst"
}

func writeHTMLLocked(dataDir, key string, data []byte, algorithm string) error {
	if len(data) > compression.MaxTextBytes {
		return compression.ErrSizeLimit
	}
	encoded, err := compression.Compress(algorithm, data)
	if err != nil {
		return err
	}
	path := strings.TrimSuffix(dataFilePath(dataDir, htmlSubdir, key), ".gz") + compression.Extension(algorithm)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	stored, err := os.ReadFile(file.Name())
	if err != nil {
		return err
	}
	decoded, err := compression.Decompress(algorithm, stored, compression.MaxTextBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(decoded, data) {
		return errors.New("compressed HTML verification failed")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func (ds *dataStore) recompress(ctx context.Context, algorithm string) (int, error) {
	if !compression.Valid(algorithm) {
		return 0, fmt.Errorf("unsupported compression algorithm %q", algorithm)
	}
	root := filepath.Join(ds.dir, htmlSubdir)
	converted := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		ext := filepath.Ext(path)
		sourceAlgorithm := compression.FromExtension(ext)
		if entry.IsDir() || sourceAlgorithm == "" || sourceAlgorithm == algorithm {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := splitPath(relative)
		if len(parts) != 4 {
			return nil
		}
		key := parts[0] + parts[1] + parts[2] + strings.TrimSuffix(parts[3], ext)
		if !validDataKey(key) {
			return nil
		}
		mu := ds.shard(key)
		mu.Lock()
		defer mu.Unlock()
		encoded, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		data, err := compression.Decompress(sourceAlgorithm, encoded, compression.MaxTextBytes)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != key {
			return errors.New("historical HTML hash verification failed")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeHTMLLocked(ds.dir, key, data, algorithm); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		converted++
		return nil
	})
	return converted, err
}
