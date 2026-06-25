// Package store reads upload state from the tusd filestore directory and
// manages the sidecar files (extracted video metadata and thumbnails) that the
// service writes alongside each upload. No database is involved: the directory
// is the single source of truth.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when an upload with the given ID does not exist.
var ErrNotFound = errors.New("upload not found")

// Store provides read access to uploads and their sidecar files within a
// single data directory shared with tusd's filestore.
type Store struct {
	dir string
}

// New returns a Store rooted at the given data directory.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// Upload is the public view of an upload's state, combining tusd's .info file
// with any sidecar metadata produced after completion.
type Upload struct {
	ID           string          `json:"id"`
	Filename     string          `json:"filename"`
	FileType     string          `json:"filetype"`
	Title        string          `json:"title"`
	Tags         []string        `json:"tags"`
	Size         int64           `json:"size"`
	Offset       int64           `json:"offset"`
	Percent      float64         `json:"percent"`
	Completed    bool            `json:"completed"`
	Duration     float64         `json:"duration"`
	CreatedAt    time.Time       `json:"created_at"`
	HasThumbnail bool            `json:"has_thumbnail"`
	ThumbnailT   *float64        `json:"thumbnail_t,omitempty"`
	VideoMeta    json.RawMessage `json:"video_meta,omitempty"`
}

// userMeta holds the user-editable fields stored in the {id}.user.json sidecar.
type userMeta struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	// ThumbnailT is the timestamp (seconds) the thumbnail was generated from,
	// when chosen via the frame picker. nil means the default/auto thumbnail.
	ThumbnailT *float64 `json:"thumbnail_t,omitempty"`
}

// fileInfo mirrors the subset of tusd's handler.FileInfo that is serialized
// into each .info file. Field names match tusd's default JSON marshaling.
type fileInfo struct {
	ID             string            `json:"ID"`
	Size           int64             `json:"Size"`
	SizeIsDeferred bool              `json:"SizeIsDeferred"`
	Offset         int64             `json:"Offset"`
	MetaData       map[string]string `json:"MetaData"`
}

// DataPath returns the path to the raw uploaded bytes for the given ID.
func (s *Store) DataPath(id string) string { return filepath.Join(s.dir, id) }

// InfoPath returns the path to tusd's .info file for the given ID.
func (s *Store) InfoPath(id string) string { return filepath.Join(s.dir, id+".info") }

// MetaPath returns the path to the extracted video-metadata sidecar.
func (s *Store) MetaPath(id string) string { return filepath.Join(s.dir, id+".meta.json") }

// ThumbPath returns the path to the generated thumbnail sidecar.
func (s *Store) ThumbPath(id string) string { return filepath.Join(s.dir, id+".jpg") }

// UserPath returns the path to the user-editable metadata sidecar (title/tags).
func (s *Store) UserPath(id string) string { return filepath.Join(s.dir, id+".user.json") }

// ProxyPath returns the cache path for a transcoded proxy with the given
// parameters. Each distinct parameter set is cached separately.
func (s *Store) ProxyPath(id string, fps float64, width int, gray bool) string {
	g := 0
	if gray {
		g = 1
	}
	name := fmt.Sprintf("%s.proxy_fps%s_w%d_g%d.mp4", id, strconv.FormatFloat(fps, 'g', -1, 64), width, g)
	return filepath.Join(s.dir, name)
}

// List returns every upload found in the data directory, newest first.
func (s *Store) List() ([]Upload, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Upload{}, nil
		}
		return nil, fmt.Errorf("read data dir: %w", err)
	}

	uploads := make([]Upload, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".info") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".info")
		up, err := s.Get(id)
		if err != nil {
			// Skip uploads whose info file is unreadable rather than failing
			// the whole listing.
			continue
		}
		uploads = append(uploads, up)
	}

	sort.Slice(uploads, func(i, j int) bool {
		return uploads[i].CreatedAt.After(uploads[j].CreatedAt)
	})
	return uploads, nil
}

// Get returns the state of a single upload by ID.
func (s *Store) Get(id string) (Upload, error) {
	raw, err := os.ReadFile(s.InfoPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Upload{}, ErrNotFound
		}
		return Upload{}, fmt.Errorf("read info: %w", err)
	}

	var info fileInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return Upload{}, fmt.Errorf("parse info: %w", err)
	}

	// tusd's filestore does not persist Offset into the .info file; it derives
	// the real offset from the size of the data file on every read. We do the
	// same so progress is always accurate.
	offset := info.Offset
	if fi, err := os.Stat(s.DataPath(id)); err == nil {
		offset = fi.Size()
	}

	up := Upload{
		ID:        id,
		Filename:  info.MetaData["filename"],
		FileType:  info.MetaData["filetype"],
		Size:      info.Size,
		Offset:    offset,
		Completed: !info.SizeIsDeferred && info.Size > 0 && offset >= info.Size,
	}
	if info.Size > 0 {
		up.Percent = float64(offset) / float64(info.Size) * 100
	}

	// CreatedAt is derived from the info file's modification time, which is a
	// good enough proxy without a database.
	if fi, err := os.Stat(s.InfoPath(id)); err == nil {
		up.CreatedAt = fi.ModTime()
	}

	if meta, err := os.ReadFile(s.MetaPath(id)); err == nil {
		up.VideoMeta = json.RawMessage(meta)
		up.Duration = parseDuration(meta)
	}
	if _, err := os.Stat(s.ThumbPath(id)); err == nil {
		up.HasThumbnail = true
	}

	user := s.readUser(id)
	up.Title = user.Title
	up.Tags = user.Tags
	up.ThumbnailT = user.ThumbnailT
	if up.Tags == nil {
		up.Tags = []string{}
	}

	return up, nil
}

// parseDuration extracts format.duration (in seconds) from ffprobe JSON output.
func parseDuration(meta []byte) float64 {
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(meta, &probe); err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return d
}

// readUser loads the user metadata sidecar, returning a zero value if absent.
func (s *Store) readUser(id string) userMeta {
	var u userMeta
	if raw, err := os.ReadFile(s.UserPath(id)); err == nil {
		_ = json.Unmarshal(raw, &u)
	}
	return u
}

// SetUserMeta persists the title and tags for an upload. The upload must exist.
// Tags are normalized: trimmed, de-duplicated and empty entries dropped.
func (s *Store) SetUserMeta(id, title string, tags []string) error {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	u := s.readUser(id) // preserve fields we are not editing (e.g. ThumbnailT)
	u.Title = strings.TrimSpace(title)
	u.Tags = normalizeTags(tags)
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return os.WriteFile(s.UserPath(id), raw, 0o644)
}

// SetThumbnailT records the timestamp (seconds) the current thumbnail was
// generated from, preserving the upload's title and tags.
func (s *Store) SetThumbnailT(id string, t float64) error {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	u := s.readUser(id)
	u.ThumbnailT = &t
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return os.WriteFile(s.UserPath(id), raw, 0o644)
}

// AllTags returns the sorted, de-duplicated union of tags across all uploads.
func (s *Store) AllTags() ([]string, error) {
	uploads, err := s.List()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, up := range uploads {
		for _, t := range up.Tags {
			seen[t] = struct{}{}
		}
	}
	tags := make([]string, 0, len(seen))
	for t := range seen {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags, nil
}

// normalizeTags trims, drops empties and removes duplicates, preserving order.
func normalizeTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// Delete removes an upload's data file and all associated sidecars. It is
// idempotent with respect to already-missing files.
func (s *Store) Delete(id string) error {
	if _, err := os.Stat(s.InfoPath(id)); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	paths := []string{s.DataPath(id), s.InfoPath(id), s.MetaPath(id), s.ThumbPath(id), s.UserPath(id)}
	// Proxy caches have parameter-dependent names, so collect them by glob.
	if proxies, err := filepath.Glob(filepath.Join(s.dir, id+".proxy_*.mp4")); err == nil {
		paths = append(paths, proxies...)
	}
	var firstErr error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
