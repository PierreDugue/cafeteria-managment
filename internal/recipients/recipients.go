// Package recipients groups selected children by parent: one email per
// parent, whatever the number of their children (SPEC §4.4).
package recipients

import (
	"sort"

	"cafeteria-management/internal/store"
	"cafeteria-management/internal/textnorm"
)

// Recipient is one parent to email and the selected children they hear about.
type Recipient struct {
	ParentID   int64
	ParentName string
	Email      string
	Children   []string
}

// Group returns one Recipient per parent, ordered by normalized parent name
// then email. Children names keep the order of the input. Entries without a
// valid email (ParentID 0) are skipped: nothing can be sent for them.
func Group(children []store.Child) []Recipient {
	out := []Recipient{}
	index := map[int64]int{}
	for _, c := range children {
		if !c.CanEmail() {
			continue
		}
		i, ok := index[c.ParentID]
		if !ok {
			i = len(out)
			index[c.ParentID] = i
			out = append(out, Recipient{ParentID: c.ParentID, ParentName: c.ParentName, Email: c.Email})
		}
		out[i].Children = append(out[i].Children, c.Name)
	}

	keys := make(map[int64]string, len(out))
	for _, r := range out {
		keys[r.ParentID] = textnorm.Key(r.ParentName)
	}
	sort.SliceStable(out, func(a, b int) bool {
		ka, kb := keys[out[a].ParentID], keys[out[b].ParentID]
		if ka != kb {
			return ka < kb
		}
		return out[a].Email < out[b].Email
	})
	return out
}
