//go:build !windows

package main

import (
	"path/filepath"
	"sync"

	"github.com/Kamkmgamer/KamkmDirStat/internal/fslist"
)

// walk lists folders by path, with up to fslist.Workers folders at a time.
func (w *localWalk) walk(root *Node, rootPath string) {
	device := fslist.DeviceOf(rootPath)
	sem := make(chan struct{}, fslist.Workers)
	var wg sync.WaitGroup
	var visit func(node *Node, dir string)
	visit = func(node *Node, dir string) {
		if w.ctx.Err() != nil {
			return
		}
		w.current.Store(dir)
		entries, err := fslist.List(dir, device)
		subs := w.add(node, dir, entries, err)
		i := 0
		for _, e := range entries {
			if e.Kind != fslist.Dir {
				continue
			}
			child, path := subs[i], filepath.Join(dir, e.Name)
			i++
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					visit(child, path)
				}()
			default:
				visit(child, path)
			}
		}
	}
	visit(root, rootPath)
	wg.Wait()
}
