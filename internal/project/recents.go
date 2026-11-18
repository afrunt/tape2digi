package project

import (
	"sort"
	"time"

	"github.com/afrunt/tape2digi/internal/appdata"
)

const (
	recentFile = "recent-projects.json"
	maxRecent  = 10
)

// RecentEntry is a remembered project. Available is computed on listing.
type RecentEntry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	LastOpened time.Time `json:"lastOpened"`
	Available  bool      `json:"available"`
}

// Recents persists the recently opened projects in the app data directory.
type Recents struct{ dir appdata.Dir }

// NewRecents creates a store backed by dir.
func NewRecents(dir appdata.Dir) *Recents { return &Recents{dir: dir} }

func (r *Recents) load() []RecentEntry {
	var list []RecentEntry
	if err := appdata.ReadJSON(r.dir.File(recentFile), &list); err != nil {
		return nil
	}
	return list
}

func (r *Recents) save(list []RecentEntry) error {
	if err := r.dir.Ensure(); err != nil {
		return err
	}
	return appdata.WriteJSON(r.dir.File(recentFile), list)
}

// List returns entries newest first. Entries whose project is gone are kept
// and marked unavailable so the user can remove them.
func (r *Recents) List() []RecentEntry {
	list := r.load()
	sort.SliceStable(list, func(i, j int) bool { return list[i].LastOpened.After(list[j].LastOpened) })
	for i := range list {
		_, err := Open(list[i].Path)
		list[i].Available = err == nil
	}
	if list == nil {
		list = []RecentEntry{}
	}
	return list
}

// Touch records that the project was opened now, updating its name.
func (r *Recents) Touch(path, name string) error {
	list := r.load()
	out := []RecentEntry{{Path: path, Name: name, LastOpened: time.Now().UTC()}}
	for _, e := range list {
		if e.Path != path {
			out = append(out, e)
		}
	}
	if len(out) > maxRecent {
		out = out[:maxRecent]
	}
	return r.save(out)
}

// Remove forgets a project path (the project itself is not touched).
func (r *Recents) Remove(path string) error {
	var out []RecentEntry
	for _, e := range r.load() {
		if e.Path != path {
			out = append(out, e)
		}
	}
	return r.save(out)
}

// SyncName updates the stored name of an entry after a rename.
func (r *Recents) SyncName(path, name string) error {
	list := r.load()
	for i := range list {
		if list[i].Path == path {
			list[i].Name = name
		}
	}
	return r.save(list)
}
