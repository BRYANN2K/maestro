package tui

import (
	"path/filepath"
	"sort"
	"strings"
)

type treeEntry struct {
	Path  string
	Name  string
	Depth int
	Dir   bool
}

type treeNode struct {
	Path     string
	Name     string
	Dir      bool
	Children map[string]*treeNode
}

func (s *IDEState) treeEntries() []treeEntry {
	if s.treeCacheValid {
		return s.treeCache
	}
	root := &treeNode{Children: map[string]*treeNode{}}
	for _, file := range s.files() {
		parts := strings.Split(filepath.ToSlash(file), "/")
		parent := root
		for i, part := range parts {
			path := strings.Join(parts[:i+1], "/")
			node := parent.Children[part]
			if node == nil {
				node = &treeNode{
					Path:     path,
					Name:     part,
					Dir:      i < len(parts)-1,
					Children: map[string]*treeNode{},
				}
				parent.Children[part] = node
			}
			parent = node
		}
	}

	var entries []treeEntry
	var walk func(*treeNode, int)
	walk = func(parent *treeNode, depth int) {
		children := make([]*treeNode, 0, len(parent.Children))
		for _, child := range parent.Children {
			children = append(children, child)
		}
		sort.Slice(children, func(i, j int) bool {
			if children[i].Dir != children[j].Dir {
				return children[i].Dir
			}
			return strings.ToLower(children[i].Name) < strings.ToLower(children[j].Name)
		})
		for _, child := range children {
			entries = append(entries, treeEntry{Path: child.Path, Name: child.Name, Depth: depth, Dir: child.Dir})
			if child.Dir && s.treeExpanded[child.Path] {
				walk(child, depth+1)
			}
		}
	}
	walk(root, 0)
	s.treeCache = entries
	s.treeCacheValid = true
	return entries
}

func (s *IDEState) toggleTree(path string) {
	if s.treeExpanded == nil {
		s.treeExpanded = map[string]bool{}
	}
	s.treeExpanded[path] = !s.treeExpanded[path]
	s.treeCacheValid = false
}
