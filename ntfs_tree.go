package main

import "github.com/Kamkmgamer/KamkmDirStat/internal/ntfs"

// addMFTToTree adds everything beneath the record root to the tree under top and returns how
// many files it added. The NTFS system files, records 0 to 15 other than the root folder,
// are left out because folder listings do not show them either.
func addMFTToTree(idx *ntfs.Index, t *Tree, top *Node, root uint32) (files int) {
	const (
		unknown uint8 = iota
		inside
		outside
	)
	state := make([]uint8, len(idx.Entries))
	nodes := make(map[uint32]*Node)
	state[root], nodes[root] = inside, top
	var chain []uint32
	// resolve returns the tree node for directory record r, creating it and any missing
	// parents, or nil if r is not beneath root.
	resolve := func(r uint32) *Node {
		chain = chain[:0]
		for {
			if int(r) >= len(state) {
				break
			}
			if state[r] == inside {
				break
			}
			e := &idx.Entries[r]
			if state[r] == outside || r < 16 || e.Flags&(ntfs.Used|ntfs.Dir|ntfs.Named) != ntfs.Used|ntfs.Dir|ntfs.Named ||
				e.Flags&ntfs.Link != 0 || len(chain) > 4096 {
				break
			}
			chain = append(chain, r)
			parent := e.Parent
			if int(parent) >= len(idx.Entries) || idx.Entries[parent].Seq != e.ParentSeq || parent == r {
				r = uint32(len(state)) // the parent was deleted and its record reused
				continue
			}
			r = parent
		}
		var node *Node
		if int(r) < len(state) && state[r] == inside {
			node = nodes[r]
		}
		for i := len(chain) - 1; i >= 0; i-- {
			c := chain[i]
			if node == nil {
				state[c] = outside
				continue
			}
			node = t.addChild(node, idx.Entries[c].Name, true, 0)
			state[c], nodes[c] = inside, node
		}
		return node
	}
	for r := range idx.Entries {
		e := &idx.Entries[r]
		if r < 16 || e.Flags&(ntfs.Used|ntfs.Named) != ntfs.Used|ntfs.Named || e.Flags&ntfs.Link != 0 {
			continue
		}
		if e.Flags&ntfs.Dir != 0 {
			resolve(uint32(r))
			continue
		}
		parent := e.Parent
		if int(parent) >= len(idx.Entries) || idx.Entries[parent].Seq != e.ParentSeq {
			continue
		}
		if node := resolve(parent); node != nil {
			t.addChild(node, e.Name, false, e.Size)
			files++
		}
	}
	return files
}
