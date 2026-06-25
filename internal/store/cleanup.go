package store

import (
	"time"
)

// CleanupIncomplete removes uploads that are still unfinished and older than
// the given TTL. Completed uploads are always preserved. It returns the number
// of uploads removed.
func (s *Store) CleanupIncomplete(ttl time.Duration) (int, error) {
	uploads, err := s.List()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-ttl)
	removed := 0
	for _, up := range uploads {
		if up.Completed || up.CreatedAt.After(cutoff) {
			continue
		}
		if err := s.Delete(up.ID); err == nil {
			removed++
		}
	}
	return removed, nil
}
