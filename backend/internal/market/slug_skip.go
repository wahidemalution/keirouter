package market

import "sync"

// SlugSkips holds the market slugs Surplus reported out of stock. Stock is
// per Surplus provider and the same for every buyer key, so the list is keyed
// by slug alone. SnapshotCache.Replace clears it: the next refresh is when
// stock is looked at again. The zero value is ready to use, and a nil
// *SlugSkips is an empty list that ignores Add.
type SlugSkips struct {
	mu    sync.RWMutex
	slugs map[string]struct{}
}

// Add sets slug aside and reports whether it was not already on the list.
func (s *SlugSkips) Add(slug string) bool {
	if s == nil || slug == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugs == nil {
		s.slugs = map[string]struct{}{}
	}
	if _, ok := s.slugs[slug]; ok {
		return false
	}
	s.slugs[slug] = struct{}{}
	return true
}

func (s *SlugSkips) Has(slug string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	_, ok := s.slugs[slug]
	s.mu.RUnlock()
	return ok
}

func (s *SlugSkips) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.slugs = nil
	s.mu.Unlock()
}
