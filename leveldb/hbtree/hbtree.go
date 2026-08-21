package hbtree

import (
	"bytes"
	"sort"

	"github.com/syndtr/goleveldb/leveldb/comparer"
)

type BTreePayload struct {
	FileID int64
	Offset uint64
	Length uint32
	Seq    uint64
	Type   uint8
}

type Node struct {
	IsLeaf   bool
	Keys     [][]byte
	Payloads []BTreePayload
	Children []*Node
	Next     *Node
}

type BTree struct {
	Root *Node
	K    int // Minimum degree (e.g. 16)
	Cmp  comparer.Comparer
}

func NewBTree(k int, cmp comparer.Comparer) *BTree {
	return &BTree{
		Root: &Node{IsLeaf: true},
		K:    k,
		Cmp:  cmp,
	}
}

func (t *BTree) compare(a, b []byte) int {
	if t.Cmp != nil {
		return t.Cmp.Compare(a, b)
	}
	return bytes.Compare(a, b)
}

// Search searches for a key in the B+ Tree.
func (t *BTree) Search(key []byte) (BTreePayload, bool) {
	node := t.Root
	for {
		if node.IsLeaf {
			idx := sort.Search(len(node.Keys), func(i int) bool {
				return t.compare(node.Keys[i], key) >= 0
			})
			if idx < len(node.Keys) && t.compare(node.Keys[idx], key) == 0 {
				return node.Payloads[idx], true
			}
			return BTreePayload{}, false
		}
		// Internal node: find child
		idx := sort.Search(len(node.Keys), func(i int) bool {
			return t.compare(node.Keys[i], key) > 0
		})
		node = node.Children[idx]
	}
}

// Insert inserts a key/payload pair into the B+ Tree.
func (t *BTree) Insert(key []byte, payload BTreePayload) {
	root := t.Root
	if len(root.Keys) == 2*t.K {
		newRoot := &Node{IsLeaf: false}
		newRoot.Children = append(newRoot.Children, root)
		t.splitChild(newRoot, 0)
		t.Root = newRoot
	}
	t.insertNonFull(t.Root, key, payload)
}

func (t *BTree) splitChild(parent *Node, index int) {
	k := t.K
	child := parent.Children[index]
	newChild := &Node{IsLeaf: child.IsLeaf}

	if child.IsLeaf {
		// Split leaf node
		newChild.Keys = append(newChild.Keys, child.Keys[k:]...)
		newChild.Payloads = append(newChild.Payloads, child.Payloads[k:]...)
		child.Keys = child.Keys[:k]
		child.Payloads = child.Payloads[:k]

		newChild.Next = child.Next
		child.Next = newChild

		parent.Keys = insertKeyAt(parent.Keys, index, newChild.Keys[0])
		parent.Children = insertNodeAt(parent.Children, index+1, newChild)
	} else {
		// Split internal node
		promotedKey := child.Keys[k]

		newChild.Keys = append(newChild.Keys, child.Keys[k+1:]...)
		newChild.Children = append(newChild.Children, child.Children[k+1:]...)

		child.Keys = child.Keys[:k]
		child.Children = child.Children[:k+1]

		parent.Keys = insertKeyAt(parent.Keys, index, promotedKey)
		parent.Children = insertNodeAt(parent.Children, index+1, newChild)
	}
}

func (t *BTree) insertNonFull(node *Node, key []byte, payload BTreePayload) {
	if node.IsLeaf {
		idx := sort.Search(len(node.Keys), func(i int) bool {
			return t.compare(node.Keys[i], key) >= 0
		})
		if idx < len(node.Keys) && t.compare(node.Keys[idx], key) == 0 {
			// Key already exists, update payload only if new sequence number is higher
			if payload.Seq > node.Payloads[idx].Seq {
				node.Payloads[idx] = payload
			}
			return
		}
		node.Keys = insertKeyAt(node.Keys, idx, key)
		node.Payloads = insertPayloadAt(node.Payloads, idx, payload)
		return
	}

	idx := sort.Search(len(node.Keys), func(i int) bool {
		return t.compare(node.Keys[i], key) > 0
	})
	child := node.Children[idx]
	if len(child.Keys) == 2*t.K {
		t.splitChild(node, idx)
		if t.compare(node.Keys[idx], key) < 0 {
			idx++
		}
		child = node.Children[idx]
	}
	t.insertNonFull(child, key, payload)
}

// FirstLeaf finds the first leaf node.
func (t *BTree) FirstLeaf() *Node {
	node := t.Root
	for !node.IsLeaf {
		if len(node.Children) == 0 {
			return nil
		}
		node = node.Children[0]
	}
	return node
}

type RangeResult struct {
	Key     []byte
	Payload BTreePayload
}

// RangeSearch performs range queries (inclusive).
func (t *BTree) RangeSearch(startKey, limitKey []byte) []RangeResult {
	var node *Node
	if startKey == nil {
		node = t.FirstLeaf()
	} else {
		node = t.Root
		for !node.IsLeaf {
			if len(node.Children) == 0 {
				return nil
			}
			idx := sort.Search(len(node.Keys), func(i int) bool {
				return t.compare(node.Keys[i], startKey) > 0
			})
			node = node.Children[idx]
		}
	}

	var results []RangeResult
	for node != nil {
		for i, k := range node.Keys {
			if startKey != nil && t.compare(k, startKey) < 0 {
				continue
			}
			if limitKey != nil && t.compare(k, limitKey) > 0 {
				return results
			}
			results = append(results, RangeResult{
				Key:     k,
				Payload: node.Payloads[i],
			})
		}
		node = node.Next
	}
	return results
}

func insertKeyAt(slice [][]byte, index int, val []byte) [][]byte {
	slice = append(slice, nil)
	copy(slice[index+1:], slice[index:])
	slice[index] = val
	return slice
}

func insertPayloadAt(slice []BTreePayload, index int, val BTreePayload) []BTreePayload {
	slice = append(slice, BTreePayload{})
	copy(slice[index+1:], slice[index:])
	slice[index] = val
	return slice
}

func insertNodeAt(slice []*Node, index int, val *Node) []*Node {
	slice = append(slice, nil)
	copy(slice[index+1:], slice[index:])
	slice[index] = val
	return slice
}
