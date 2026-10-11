package visual

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Store struct {
	dir      string
	mu       sync.RWMutex
	refs     map[string]int
	pending  map[string]uint64
	revision uint64
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func ValidKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil && key == strings.ToLower(key)
}

func contentKey(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ValidateCapture(capture *Capture) error {
	if capture == nil {
		return nil
	}
	if len(capture.HTML) == 0 || len(capture.HTML) > MaxHTMLBytes || !utf8.ValidString(capture.HTML) {
		return errors.New("visual HTML is empty, invalid, or too large")
	}
	if capture.ViewportWidth < 0 || capture.ViewportWidth > 16384 || capture.ViewportHeight < 0 || capture.ViewportHeight > 16384 || len(capture.Resources) > MaxResources {
		return errors.New("invalid visual viewport or resource count")
	}
	total := len(capture.HTML)
	for _, asset := range capture.Resources {
		total += len(asset.Data) + len(asset.URL)
		parsed, err := url.Parse(asset.URL)
		if err != nil || parsed.User != nil || len(asset.URL) > 32768 || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "data" && parsed.Scheme != "blob") {
			return errors.New("invalid visual resource URL")
		}
		if (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() == "" {
			return errors.New("missing visual resource host")
		}
		mediaType, _, err := mime.ParseMediaType(asset.MIME)
		if err != nil || len(asset.Data) == 0 || len(asset.Data) > MaxResourceBytes || total > MaxCaptureBytes {
			return errors.New("invalid visual resource type or size")
		}
		switch asset.Kind {
		case "css":
			if mediaType != "text/css" || !utf8.Valid(asset.Data) {
				return errors.New("invalid visual stylesheet")
			}
		case "font":
			if !strings.HasPrefix(mediaType, "font/") && mediaType != "application/font-woff" && mediaType != "application/vnd.ms-fontobject" && mediaType != "application/octet-stream" {
				return errors.New("invalid visual font")
			}
		case "image":
			if !strings.HasPrefix(mediaType, "image/") {
				return errors.New("invalid visual image")
			}
		default:
			return errors.New("invalid visual resource kind")
		}
	}
	return nil
}

func (store *Store) Save(ctx context.Context, id string, capture *Capture) error {
	return store.save(ctx, id, capture, nil)
}

func (store *Store) SaveIfPresent(ctx context.Context, id string, capture *Capture, exists func(string) bool) error {
	return store.save(ctx, id, capture, exists)
}

func (store *Store) save(ctx context.Context, id string, capture *Capture, exists func(string) bool) error {
	if err := ValidateCapture(capture); err != nil {
		return err
	}
	if capture == nil {
		return nil
	}
	store.mu.Lock()
	if store.pending == nil {
		store.pending = make(map[string]uint64)
	}
	store.revision++
	revision := store.revision
	store.pending[id] = revision
	store.mu.Unlock()
	defer func() {
		store.mu.Lock()
		if store.pending[id] == revision {
			delete(store.pending, id)
		}
		store.mu.Unlock()
	}()
	manifest := &Manifest{Version: 1, CapturedAt: time.Now().UTC(), ViewportWidth: capture.ViewportWidth, ViewportHeight: capture.ViewportHeight, ColorScheme: capture.ColorScheme}
	assets := make([]Asset, 0, len(capture.Resources))
	seen := make(map[string]bool)
	for _, asset := range capture.Resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[asset.URL] {
			continue
		}
		seen[asset.URL] = true
		resource := Resource{URL: asset.URL, MIME: asset.MIME, Kind: asset.Kind, Width: asset.Width, Height: asset.Height}
		if asset.Kind == "image" {
			optimized, width, height, err := OptimizeImage(ctx, asset)
			if errors.Is(err, ErrUnsafeImage) {
				continue
			}
			resource.MIME = optimized.MIME
			resource.Width, resource.Height = optimized.Width, optimized.Height
			resource.StoredWidth, resource.StoredHeight = optimized.Width, optimized.Height
			if err == nil {
				asset = optimized
				resource.MIME = asset.MIME
				resource.Width, resource.Height = asset.Width, asset.Height
				resource.StoredWidth, resource.StoredHeight = width, height
			}
		}
		resource.Key = contentKey(asset.Data)
		manifest.Resources = append(manifest.Resources, resource)
		assets = append(assets, asset)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.pending[id] != revision || (exists != nil && !exists(id)) {
		return nil
	}
	if err := store.loadRefs(); err != nil {
		return err
	}
	old, err := store.readManifest(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	manifest.HTMLKey, err = store.writeBlob("html", []byte(capture.HTML))
	if err != nil {
		return err
	}
	for index, asset := range assets {
		kind := resourceDir(asset.Kind)
		if _, err := store.writeBlob(kind, asset.Data); err != nil {
			return err
		}
		manifest.Resources[index].Key = contentKey(asset.Data)
	}
	data, err := json.Marshal(storedManifest{DocumentID: id, Manifest: *manifest})
	if err != nil {
		return err
	}
	if err := writeAtomic(store.manifestPath(id), data); err != nil {
		return err
	}
	store.changeRefs(manifest, 1)
	store.changeRefs(old, -1)
	return nil
}

func (store *Store) Read(id string) (*Manifest, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.readManifest(id)
}

func (store *Store) ReadHTML(key string) ([]byte, error) { return store.readBlob("html", key) }

func (store *Store) ReadResource(key, kind string) ([]byte, error) {
	if kind != "css" && kind != "image" && kind != "font" {
		return nil, errors.New("invalid visual resource kind")
	}
	return store.readBlob(resourceDir(kind), key)
}

func (store *Store) Delete(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.pending, id)
	if err := store.loadRefs(); err != nil {
		return err
	}
	manifest, err := store.readManifest(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(store.manifestPath(id)); err != nil {
		return err
	}
	store.changeRefs(manifest, -1)
	return nil
}

func (store *Store) Cleanup(exists func(string) bool) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadRefs(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(filepath.Join(store.dir, "manifests"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(store.dir, "manifests", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return removed, err
		}
		var record storedManifest
		if err := json.Unmarshal(data, &record); err != nil {
			return removed, err
		}
		if record.DocumentID != "" && !exists(record.DocumentID) {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
			store.changeRefs(&record.Manifest, -1)
			removed++
		}
	}
	for _, kind := range []string{"html", "css", "assets"} {
		err := filepath.WalkDir(filepath.Join(store.dir, kind), func(path string, entry os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			key := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if ValidKey(key) && store.refs[kind+":"+key] == 0 {
				if err := os.Remove(path); err != nil {
					return err
				}
				removed++
			}
			return nil
		})
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

type storedManifest struct {
	DocumentID string `json:"document_id"`
	Manifest
}

func (store *Store) manifestPath(id string) string {
	return filepath.Join(store.dir, "manifests", contentKey([]byte(id))+".json")
}

func (store *Store) readManifest(id string) (*Manifest, error) {
	data, err := os.ReadFile(store.manifestPath(id))
	if err != nil {
		return nil, err
	}
	var record storedManifest
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.Version != 1 || !ValidKey(record.HTMLKey) {
		return nil, errors.New("invalid visual manifest")
	}
	return &record.Manifest, nil
}

func (store *Store) loadRefs() error {
	if store.refs != nil {
		return nil
	}
	refs := make(map[string]int)
	entries, err := os.ReadDir(filepath.Join(store.dir, "manifests"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(store.dir, "manifests", entry.Name()))
		if err != nil {
			return err
		}
		var record storedManifest
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		for _, reference := range manifestRefs(&record.Manifest) {
			refs[reference]++
		}
	}
	store.refs = refs
	return nil
}

func manifestRefs(manifest *Manifest) []string {
	if manifest == nil {
		return nil
	}
	refs := []string{"html:" + manifest.HTMLKey}
	for _, resource := range manifest.Resources {
		refs = append(refs, resourceDir(resource.Kind)+":"+resource.Key)
	}
	return refs
}

func (store *Store) changeRefs(manifest *Manifest, delta int) {
	for _, reference := range manifestRefs(manifest) {
		store.refs[reference] += delta
		if store.refs[reference] <= 0 {
			delete(store.refs, reference)
			kind, key, _ := strings.Cut(reference, ":")
			if ValidKey(key) {
				_ = os.Remove(store.blobPath(kind, key))
			}
		}
	}
}

func resourceDir(kind string) string {
	if kind == "css" {
		return "css"
	}
	return "assets"
}

func (store *Store) blobPath(kind, key string) string {
	ext := ".bin"
	if kind != "assets" {
		ext = ".zst"
	}
	return filepath.Join(store.dir, kind, key[:2], key+ext)
}

func (store *Store) writeBlob(kind string, data []byte) (string, error) {
	key := contentKey(data)
	path := store.blobPath(kind, key)
	if _, err := os.Stat(path); err == nil {
		return key, nil
	}
	if kind != "assets" {
		var err error
		data, err = CompressText(data)
		if err != nil {
			return "", err
		}
	}
	return key, writeAtomic(path, data)
}

func (store *Store) readBlob(kind, key string) ([]byte, error) {
	if !ValidKey(key) {
		return nil, errors.New("invalid visual resource key")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	data, err := os.ReadFile(store.blobPath(kind, key))
	if err != nil {
		return nil, err
	}
	if kind != "assets" {
		return DecompressText(data, MaxHTMLBytes)
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write visual archive: %w", err)
	}
	return os.Rename(file.Name(), path)
}
