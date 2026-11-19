package main

import (
	"runtime"
	"strings"
	"sync"

	"github.com/Kamkmgamer/KamkmDirStat/internal/fslist"
	"golang.org/x/sys/windows"
)

// Opening each folder by its full path makes Windows look up every folder along the way.
// This walk opens each folder relative to its parent's open handle instead, and reads its
// entries in large batches, which roughly halves the time. A folder is opened only when a
// worker takes it, so few handles are open at once: each parent stays open until all of its
// subfolders have been opened.

type walkJob struct {
	parent *fslist.Parent // nil for the scan root
	name   string         // relative to parent, or the full NT path of the root
	path   string         // for messages
	node   *Node
}

// walk lists the folders beneath rootPath with a pool of workers that take the newest job
// first, which keeps the number of waiting folders small.
func (w *localWalk) walk(root *Node, rootPath string) {
	workers := min(16, max(8, runtime.NumCPU()))
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	stack := []walkJob{{name: fslist.NTRoot(rootPath), path: rootPath, node: root}}
	active := 0
	push := func(jobs []walkJob) {
		mu.Lock()
		stack = append(stack, jobs...)
		mu.Unlock()
		cond.Broadcast()
	}
	buffers := sync.Pool{New: func() any { return fslist.NewBuffer() }}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(stack) == 0 && active > 0 {
					cond.Wait()
				}
				if len(stack) == 0 {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				job := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				active++
				mu.Unlock()
				w.visit(job, push, &buffers)
				mu.Lock()
				active--
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
}

func (w *localWalk) visit(job walkJob, push func([]walkJob), buffers *sync.Pool) {
	var root windows.Handle
	if job.parent != nil {
		root = job.parent.H
	}
	if w.ctx.Err() != nil {
		job.parent.Release()
		return
	}
	h, err := fslist.OpenRelative(root, job.name)
	job.parent.Release()
	if err != nil {
		w.add(job.node, job.path, nil, err)
		return
	}
	w.current.Store(job.path)
	buf := buffers.Get().([]byte)
	entries, err := fslist.Read(h, buf)
	buffers.Put(buf)
	subs := w.add(job.node, job.path, entries, err)
	if len(subs) == 0 {
		windows.CloseHandle(h)
		return
	}
	parent := fslist.Hold(h, len(subs))
	jobs := make([]walkJob, 0, len(subs))
	i := 0
	for _, e := range entries {
		if e.Kind == fslist.Dir {
			jobs = append(jobs, walkJob{parent: parent, name: e.Name, path: job.path + `\` + e.Name, node: subs[i]})
			i++
		}
	}
	if strings.HasSuffix(job.path, `\`) { // a drive root such as C:\
		for k := range jobs {
			jobs[k].path = job.path + jobs[k].name
		}
	}
	push(jobs)
}
