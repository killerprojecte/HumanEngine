package jsjson

import (
	"sort"
	"strconv"
)

// Member is an object's own enumerable string-keyed data property.
type Member struct {
	Key   string
	Value any
}

// Object preserves string-property insertion order, unlike a Go map. Its zero
// value is an empty object. Use Set/Delete, not a map conversion, when order is
// significant. Values are the types documented by Stringify.
// Like Go maps, Object is not safe for concurrent mutation. Do not copy an
// initialized Object by value; use Clone for independent property storage.
type Object struct {
	members []Member
	index   map[string]int
}

// NewObject applies members in order. Duplicate keys replace values without
// changing the position of the original key, just like JSON.parse.
func NewObject(members ...Member) (*Object, error) {
	o := new(Object)
	for _, member := range members {
		if err := o.Set(member.Key, member.Value); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// Set creates or replaces a property. Keys must be canonical WTF-8. Values are
// checked when encoded, allowing ordinary construction of arrays and objects.
func (o *Object) Set(key string, value any) error {
	if err := ValidateString(key); err != nil {
		return err
	}
	o.set(key, value)
	return nil
}

func (o *Object) set(key string, value any) {
	if i, ok := o.index[key]; ok {
		o.members[i].Value = value
		return
	}
	if o.index == nil {
		o.index = make(map[string]int)
	}
	o.index[key] = len(o.members)
	o.members = append(o.members, Member{Key: key, Value: value})
}

func (o *Object) Get(key string) (any, bool) {
	i, ok := o.index[key]
	if !ok {
		return nil, false
	}
	return o.members[i].Value, true
}

// Delete removes a property. Setting that key again gives it a new insertion
// position, as in JavaScript. Integer-index keys always enumerate numerically.
func (o *Object) Delete(key string) bool {
	i, ok := o.index[key]
	if !ok {
		return false
	}
	delete(o.index, key)
	copy(o.members[i:], o.members[i+1:])
	o.members[len(o.members)-1] = Member{}
	o.members = o.members[:len(o.members)-1]
	for j := i; j < len(o.members); j++ {
		o.index[o.members[j].Key] = j
	}
	return true
}

func (o *Object) Len() int { return len(o.members) }

// Clone copies the property's insertion order and values into a new object.
// This is a shallow copy: nested *Object and []any values remain shared.
func (o *Object) Clone() *Object {
	clone := new(Object)
	for _, member := range o.members {
		clone.set(member.Key, member.Value)
	}
	return clone
}

// Members returns a shallow copy in Object.keys / JSON.stringify order:
// canonical integer indices 0..2^32-2 first, ascending, then other keys in
// insertion order. "4294967295", "01", and "-0" are not integer indices.
func (o *Object) Members() []Member {
	members := append([]Member{}, o.members...)
	sort.SliceStable(members, func(i, j int) bool {
		a, aIndex := arrayIndex(members[i].Key)
		b, bIndex := arrayIndex(members[j].Key)
		if aIndex != bIndex {
			return aIndex
		}
		return aIndex && a < b
	})
	return members
}

func (o *Object) Keys() []string {
	members := o.Members()
	keys := make([]string, len(members))
	for i, member := range members {
		keys[i] = member.Key
	}
	return keys
}

func arrayIndex(key string) (uint64, bool) {
	if key == "" || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	for i := range key {
		if key[i] < '0' || key[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 0xffffffff
}
