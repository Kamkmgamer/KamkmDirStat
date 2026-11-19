// Package dirview defines the report of one scanned folder that the window shows: its
// largest entries and a treemap of what is beneath it. The app builds it from a scan in
// memory or in its cache file, and the native server scanner builds it on the server, so
// the shape and the limits are defined once here. remote_report.py builds the same report
// in Python and must be kept in step by hand.
package dirview

type Crumb struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Entry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Directory  bool   `json:"dir,omitempty"`
	ID         int    `json:"id,omitempty"`
	Files      int    `json:"files,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

// MapNode is the treemap data. Short keys keep the payload small; O is the combined size of
// children too small to draw.
type MapNode struct {
	N string     `json:"n"`
	S int64      `json:"s"`
	D bool       `json:"d,omitempty"`
	I int        `json:"i,omitempty"`
	C []*MapNode `json:"c,omitempty"`
	O int64      `json:"o,omitempty"`
}

type View struct {
	ID         int      `json:"id"`
	Trail      []Crumb  `json:"trail"`
	Size       int64    `json:"size"`
	Files      int      `json:"files"`
	Items      int      `json:"items"`
	Incomplete bool     `json:"incomplete,omitempty"`
	Entries    []Entry  `json:"entries"`
	MoreCount  int      `json:"moreCount,omitempty"`
	MoreSize   int64    `json:"moreSize,omitempty"`
	Map        *MapNode `json:"map"`
}

const (
	EntryLimit   = 500   // entries listed for one folder; the rest are only counted
	MapNodeLimit = 12000 // blocks in one treemap
	MapDetail    = 4000  // entries smaller than 1/MapDetail of the folder are merged
)

// Item is what a view needs to know about one scanned entry. ID is a folder's number in
// the scan and 0 for files.
type Item struct {
	Name       string
	Size       int64
	Files      int
	Directory  bool
	ID         int
	Incomplete bool
}

// Source reads a scan whose entries are held as values of type N.
type Source[N any] interface {
	Item(n N) Item
	// Children returns a folder's entries, largest first.
	Children(n N) ([]N, error)
}

// Build makes the view of one folder. trail names the folders from the scan root down to
// it, and kids are its entries, largest first.
func Build[N any](src Source[N], trail []Crumb, folder N, kids []N) (View, error) {
	f := src.Item(folder)
	v := View{ID: f.ID, Trail: trail, Size: f.Size, Files: f.Files, Items: len(kids), Incomplete: f.Incomplete, Entries: []Entry{}}
	for i, kid := range kids {
		c := src.Item(kid)
		if i >= EntryLimit {
			v.MoreCount++
			v.MoreSize += c.Size
			continue
		}
		e := Entry{Name: c.Name, Size: c.Size, Directory: c.Directory, Files: c.Files, Incomplete: c.Incomplete}
		if c.Directory {
			e.ID = c.ID
		}
		v.Entries = append(v.Entries, e)
	}
	// The map is filled breadth first so shallow levels get detail before deep ones when
	// the block budget runs out.
	v.Map = &MapNode{N: f.Name, S: f.Size, D: true, I: f.ID}
	threshold := max(f.Size/MapDetail, 1)
	type level struct {
		node N
		m    *MapNode
		kids []N
		read bool
	}
	queue := []level{{folder, v.Map, kids, true}}
	budget := MapNodeLimit
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if !it.read {
			var err error
			if it.kids, err = src.Children(it.node); err != nil {
				return View{}, err
			}
		}
		for _, kid := range it.kids {
			c := src.Item(kid)
			if c.Size < threshold || budget <= 0 {
				it.m.O += c.Size
				continue
			}
			budget--
			cm := &MapNode{N: c.Name, S: c.Size, D: c.Directory}
			if c.Directory {
				cm.I = c.ID
				queue = append(queue, level{node: kid, m: cm})
			}
			it.m.C = append(it.m.C, cm)
		}
	}
	return v, nil
}
