package cli

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/client"
)

// trashNode is one name in the deleted namespace.
//
// The bin arrives as a flat list of opaque keys, but every entry carries the
// path it used to have, which is enough to rebuild the shape the user remembers.
// Navigating that shape is the whole point: it turns "forty thousand keys" into
// a directory with forty thousand things in it, and it means nobody ever has to
// see a key, let alone copy one.
type trashNode struct {
	name     string
	parent   *trashNode
	children []*trashNode
	byName   map[string]*trashNode

	// entry is set when this node is itself a deleted item. An intermediate
	// directory that was never deleted — only emptied — has none.
	entry *client.TrashItem

	// Aggregates over this node and everything below it.
	entries int
	size    int64
	newest  time.Time
}

// wholeFolder reports whether this node was deleted as a tree, which the storage
// records as a single entry. Restoring it brings back everything underneath in
// one request, so it is by far the cheapest thing a user can do.
func (n *trashNode) wholeFolder() bool {
	return n.entry != nil && n.entry.IsDir
}

// isDir reports whether the node has children or was a directory.
func (n *trashNode) isDir() bool {
	return len(n.children) > 0 || (n.entry != nil && n.entry.IsDir)
}

// buildTrashTree arranges deleted items by their original location.
func buildTrashTree(items []client.TrashItem) *trashNode {
	root := &trashNode{byName: map[string]*trashNode{}}

	for i := range items {
		it := items[i]
		segs := trashPathSegments(it.OriginalPath)
		if len(segs) == 0 {
			// Nowhere to put an item the server gave no location for. Hiding it
			// would be worse than showing it at the top, since it is still
			// restorable and still occupying quota.
			segs = []string{trashDisplayName(it)}
		}
		node := root
		for _, seg := range segs {
			child, ok := node.byName[seg]
			if !ok {
				child = &trashNode{name: seg, parent: node, byName: map[string]*trashNode{}}
				node.byName[seg] = child
				node.children = append(node.children, child)
			}
			node = child
		}
		// Two deletions of the same path keep the newer one, and the older
		// becomes unreachable in the tree. Recording that would mean a list per
		// node; for now the newest is what a restore should bring back anyway.
		if node.entry == nil || it.DeletedAt.After(node.entry.DeletedAt) {
			entry := it
			node.entry = &entry
		}
	}

	aggregate(root)
	sortTrashTree(root)
	return root
}

// aggregate fills in the subtree totals, deepest first.
func aggregate(n *trashNode) {
	n.entries, n.size, n.newest = 0, 0, time.Time{}
	if n.entry != nil {
		n.entries, n.size, n.newest = 1, n.entry.Size, n.entry.DeletedAt
	}
	for _, c := range n.children {
		aggregate(c)
		n.entries += c.entries
		n.size += c.size
		if c.newest.After(n.newest) {
			n.newest = c.newest
		}
	}
}

// sortTrashTree orders each level the way a file manager does: directories
// first, then by name, so the shape is stable between runs.
func sortTrashTree(n *trashNode) {
	slices.SortFunc(n.children, func(a, b *trashNode) int {
		if a.isDir() != b.isDir() {
			if a.isDir() {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.name, b.name)
	})
	for _, c := range n.children {
		sortTrashTree(c)
	}
}

// find walks to a node by its path segments, returning nil when the path is no
// longer in the tree — which happens after a reload with a narrower window.
func (n *trashNode) find(segs []string) *trashNode {
	node := n
	for _, s := range segs {
		child, ok := node.byName[s]
		if !ok {
			return nil
		}
		node = child
	}
	return node
}

// subtreeEntries collects every deleted item at or below a node.
func (n *trashNode) subtreeEntries() []client.TrashItem {
	var out []client.TrashItem
	var walk func(*trashNode)
	walk = func(node *trashNode) {
		if node.entry != nil {
			out = append(out, *node.entry)
		}
		for _, c := range node.children {
			walk(c)
		}
	}
	walk(n)
	return out
}

// trashPathSegments splits an original location into names.
func trashPathSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

func trashDisplayName(it client.TrashItem) string {
	if it.Name != "" {
		return it.Name
	}
	return it.Key
}

// trashPlan is what a restore is about to do.
type trashPlan struct {
	// Items are the restores to perform, parents before children.
	Items []client.TrashItem
	// Covered counts items dropped because a directory above them is being
	// restored too, which brings them back by itself.
	Covered int
}

// buildTrashPlan turns a selection into an ordered set of restores.
//
// Two corrections a user should not have to make by hand. A directory deleted
// whole restores everything under it, so restoring both it and its contents
// would repeat work and then fail on the children, whose paths already exist by
// the time their turn comes. And a child cannot be restored before its parent
// directory exists: the server answers 409, so depth is the order.
func buildTrashPlan(sel []client.TrashItem) trashPlan {
	var covers []string
	for _, it := range sel {
		if it.IsDir {
			covers = append(covers, trashPathKey(it.OriginalPath))
		}
	}

	plan := trashPlan{}
	for _, it := range sel {
		if coveredByAncestor(trashPathKey(it.OriginalPath), covers) {
			plan.Covered++
			continue
		}
		plan.Items = append(plan.Items, it)
	}

	slices.SortStableFunc(plan.Items, func(a, b client.TrashItem) int {
		da, db := len(trashPathSegments(a.OriginalPath)), len(trashPathSegments(b.OriginalPath))
		if da != db {
			return cmp.Compare(da, db)
		}
		return cmp.Compare(a.OriginalPath, b.OriginalPath)
	})
	return plan
}

// coveredByAncestor reports whether p sits under one of the covering paths, and
// is not the covering path itself.
func coveredByAncestor(p string, covers []string) bool {
	for _, c := range covers {
		if p != c && strings.HasPrefix(p, c+"/") {
			return true
		}
	}
	return false
}

// trashPathKey normalises a location for prefix comparison, so that "a/b" and
// "/a/b/" are the same path and "ab" is not under "a".
func trashPathKey(p string) string {
	return strings.Join(trashPathSegments(p), "/")
}
