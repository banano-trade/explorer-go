// Package known holds public names for Banano accounts (exchanges, faucets, reps...).
package known

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Account struct {
	Address  string `json:"address"`
	Alias    string `json:"alias"`
	Owner    string `json:"owner,omitempty"`
	Category string `json:"category"`
}

// Store maps addresses to names. Safe for concurrent use; Load can be called again to refresh.
type Store struct {
	mu     sync.RWMutex
	by     map[string]Account
	list   []Account
	counts map[string]int
}

// Categories that are only used for avatars or blocking, not shown as names.
var skip = map[string]bool{"vanity": true, "blocklist": true}

//go:embed accounts/*.json
var builtin embed.FS

// Load reads every <category>.json file in dir (the spyglass known-accounts
// format). An empty dir uses the list built into the binary.
func Load(dir string) (*Store, error) {
	s := &Store{}
	return s, s.Reload(dir)
}

func (s *Store) Reload(dir string) error {
	var fsys fs.FS = os.DirFS(dir)
	if dir == "" {
		fsys, _ = fs.Sub(builtin, "accounts")
	}
	files, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return err
	}
	by := map[string]Account{}
	counts := map[string]int{}
	sort.Strings(files)
	for _, f := range files {
		cat := strings.TrimSuffix(filepath.Base(f), ".json")
		if skip[cat] {
			continue
		}
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return err
		}
		var items []Account
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
		for _, a := range items {
			if a.Address == "" || a.Alias == "" {
				continue
			}
			if _, dup := by[a.Address]; dup {
				continue
			}
			a.Alias = strings.ReplaceAll(a.Alias, "[.]", ".") // spyglass defangs links
			a.Category = cat
			by[a.Address] = a
			counts[cat]++
		}
	}
	list := make([]Account, 0, len(by))
	for _, a := range by {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Category != list[j].Category {
			return list[i].Category < list[j].Category
		}
		return strings.ToLower(list[i].Alias) < strings.ToLower(list[j].Alias)
	})
	s.mu.Lock()
	s.by, s.list, s.counts = by, list, counts
	s.mu.Unlock()
	return nil
}

func (s *Store) Get(addr string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.by[addr]
	return a, ok
}

// Name returns the alias or "" for unknown addresses.
func (s *Store) Name(addr string) string {
	a, _ := s.Get(addr)
	return a.Alias
}

func (s *Store) All() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.list
}

func (s *Store) Counts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.counts
}

// IsBanano is true for accounts owned by the Banano team (funds, wBAN wallets).
func (s *Store) IsBanano(addr string) bool {
	a, ok := s.Get(addr)
	return ok && strings.EqualFold(a.Owner, "Banano")
}

// Search returns accounts whose alias contains q (case-insensitive), prefix matches first.
func (s *Store) Search(q string, limit int) []Account {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var pre, mid []Account
	for _, a := range s.All() {
		n := strings.ToLower(a.Alias)
		switch {
		case strings.HasPrefix(n, q):
			pre = append(pre, a)
		case strings.Contains(n, q):
			mid = append(mid, a)
		}
	}
	out := append(pre, mid...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
