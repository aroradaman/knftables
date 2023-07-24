/*
Copyright 2023 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nftables

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Fake is a fake implementation of Interface
type Fake struct {
	family  Family
	table   string
	defines []define

	nextHandle int

	// Table contains the Interface's table, if it has been added
	Table *FakeTable
}

// FakeTable wraps Table for the Fake implementation
type FakeTable struct {
	Table

	Chains map[string]*FakeChain
	Sets   map[string]*FakeSet
	Maps   map[string]*FakeMap
}

// FakeChain wraps Chain for the Fake implementation
type FakeChain struct {
	Chain

	Rules []*Rule
}

// FakeSet wraps Set for the Fake implementation
type FakeSet struct {
	Set

	Elements []*Element
}

// FakeMap wraps Set for the Fake implementation
type FakeMap struct {
	Map

	Elements []*Element
}

// NewFake creates a new fake Interface, for unit tests
func NewFake(family Family, table string) *Fake {
	return &Fake{
		family:  family,
		table:   table,
		defines: defaultDefinesForFamily(family),
	}
}

var _ Interface = &Fake{}

// Present is part of Interface.
func (fake *Fake) Present() error {
	return nil
}

// List is part of Interface.
func (fake *Fake) List(ctx context.Context, objectType string) ([]string, error) {
	if fake.Table == nil {
		return nil, fmt.Errorf("no such table %q", fake.table)
	}

	var result []string

	switch objectType {
	case "chain", "chains":
		for name := range fake.Table.Chains {
			result = append(result, name)
		}
	case "set", "sets":
		for name := range fake.Table.Sets {
			result = append(result, name)
		}
	case "map", "maps":
		for name := range fake.Table.Maps {
			result = append(result, name)
		}

	default:
		return nil, fmt.Errorf("unsupported object type %q", objectType)
	}

	return result, nil
}

// ListRules is part of Interface
func (fake *Fake) ListRules(ctx context.Context, chain string) ([]*Rule, error) {
	if fake.Table == nil {
		return nil, fmt.Errorf("no such chain %q", chain)
	}
	ch := fake.Table.Chains[chain]
	if ch == nil {
		return nil, fmt.Errorf("no such chain %q", chain)
	}
	return ch.Rules, nil
}

// ListElements is part of Interface
func (fake *Fake) ListElements(ctx context.Context, objectType, name string) ([]*Element, error) {
	if fake.Table == nil {
		return nil, fmt.Errorf("no such %s %q", objectType, name)
	}
	if objectType == "set" {
		s := fake.Table.Sets[name]
		if s != nil {
			return s.Elements, nil
		}
	} else if objectType == "map" {
		m := fake.Table.Maps[name]
		if m != nil {
			return m.Elements, nil
		}
	}
	return nil, fmt.Errorf("no such %s %q", objectType, name)
}

// Define is part of Interface
func (fake *Fake) Define(name, value string) {
	fake.defines = append(fake.defines, define{name, value})
}

func substituteDefines(val string, defines []define) string {
	for _, def := range defines {
		val = strings.ReplaceAll(val, "$"+def.name, def.value)
	}
	return val
}

// Run is part of Interface
func (fake *Fake) Run(ctx context.Context, tx *Transaction) error {
	if tx.err != nil {
		return tx.err
	}

	// FIXME: not actually transactional!

	for _, op := range tx.operations {
		if fake.Table == nil {
			if _, ok := op.obj.(*Table); !ok || op.verb != addVerb {
				return notFoundError("no such table \"%s %s\"", fake.family, fake.table)
			}
		}

		if op.verb == addVerb || op.verb == createVerb {
			fake.nextHandle++
		}

		switch obj := op.obj.(type) {
		case *Table:
			switch op.verb {
			case flushVerb:
				fake.Table = nil
				fallthrough
			case addVerb:
				if fake.Table == nil {
					table := *obj
					table.Handle = Optional(fake.nextHandle)
					fake.Table = &FakeTable{
						Table:  table,
						Chains: make(map[string]*FakeChain),
						Sets:   make(map[string]*FakeSet),
						Maps:   make(map[string]*FakeMap),
					}
				}
			case deleteVerb:
				fake.Table = nil
			default:
				return fmt.Errorf("unhandled operation %q", op.verb)
			}
		case *Chain:
			existingChain := fake.Table.Chains[obj.Name]
			if existingChain == nil && op.verb != addVerb {
				return notFoundError("no such chain %q", obj.Name)
			}
			switch op.verb {
			case addVerb:
				if existingChain != nil {
					continue
				}
				chain := *obj
				chain.Handle = Optional(fake.nextHandle)
				fake.Table.Chains[obj.Name] = &FakeChain{
					Chain: chain,
				}
			case flushVerb:
				existingChain.Rules = nil
			case deleteVerb:
				// FIXME delete-by-handle
				delete(fake.Table.Chains, obj.Name)
			default:
				return fmt.Errorf("unhandled operation %q", op.verb)
			}
		case *Rule:
			existingChain := fake.Table.Chains[obj.Chain]
			if existingChain == nil {
				return notFoundError("no such chain %q", obj.Chain)
			}
			switch op.verb {
			case addVerb:
				rule := *obj
				rule.Rule = substituteDefines(rule.Rule, fake.defines)
				rule.Handle = Optional(fake.nextHandle)
				existingChain.Rules = append(existingChain.Rules, &rule)
			case deleteVerb:
				if i := findRule(existingChain.Rules, *obj.Handle); i != -1 {
					existingChain.Rules = append(existingChain.Rules[:i], existingChain.Rules[i+1:]...)
				} else {
					return notFoundError("no rule with handle %d", *obj.Handle)
				}
			default:
				return fmt.Errorf("unhandled operation %q", op.verb)
			}
		case *Set:
			existingSet := fake.Table.Sets[obj.Name]
			if existingSet == nil && op.verb != addVerb {
				return notFoundError("no such set %q", obj.Name)
			}
			switch op.verb {
			case addVerb:
				if existingSet != nil {
					continue
				}
				set := *obj
				set.Type = substituteDefines(set.Type, fake.defines)
				set.TypeOf = substituteDefines(set.TypeOf, fake.defines)
				set.Handle = Optional(fake.nextHandle)
				fake.Table.Sets[obj.Name] = &FakeSet{
					Set: set,
				}
			case flushVerb:
				existingSet.Elements = nil
			case deleteVerb:
				// FIXME delete-by-handle
				delete(fake.Table.Sets, obj.Name)
			default:
				return fmt.Errorf("unhandled operation %q", op.verb)
			}
		case *Map:
			existingMap := fake.Table.Maps[obj.Name]
			if existingMap == nil && op.verb != addVerb {
				return notFoundError("no such map %q", obj.Name)
			}
			switch op.verb {
			case addVerb:
				if existingMap != nil {
					continue
				}
				mapObj := *obj
				mapObj.Type = substituteDefines(mapObj.Type, fake.defines)
				mapObj.TypeOf = substituteDefines(mapObj.TypeOf, fake.defines)
				mapObj.Handle = Optional(fake.nextHandle)
				fake.Table.Maps[obj.Name] = &FakeMap{
					Map: mapObj,
				}
			case flushVerb:
				existingMap.Elements = nil
			case deleteVerb:
				// FIXME delete-by-handle
				delete(fake.Table.Maps, obj.Name)
			default:
				return fmt.Errorf("unhandled operation %q", op.verb)
			}
		case *Element:
			if len(obj.Value) == 0 {
				existingSet := fake.Table.Sets[obj.Name]
				if existingSet == nil {
					return notFoundError("no such set %q", obj.Name)
				}
				switch op.verb {
				case addVerb:
					element := *obj
					element.Key = substituteDefines(element.Key, fake.defines)
					if i := findElement(existingSet.Elements, element.Key); i != -1 {
						existingSet.Elements[i] = &element
					} else {
						existingSet.Elements = append(existingSet.Elements, &element)
					}
				case deleteVerb:
					key := substituteDefines(obj.Key, fake.defines)
					if i := findElement(existingSet.Elements, obj.Key); i != -1 {
						existingSet.Elements = append(existingSet.Elements[:i], existingSet.Elements[i+1:]...)
					} else {
						return notFoundError("no such element %q", key)
					}
				default:
					return fmt.Errorf("unhandled operation %q", op.verb)
				}
			} else {
				existingMap := fake.Table.Maps[obj.Name]
				if existingMap == nil {
					return notFoundError("no such map %q", obj.Name)
				}
				switch op.verb {
				case addVerb:
					element := *obj
					element.Key = substituteDefines(element.Key, fake.defines)
					element.Value = substituteDefines(element.Value, fake.defines)
					if i := findElement(existingMap.Elements, element.Key); i != -1 {
						existingMap.Elements[i] = &element
					} else {
						existingMap.Elements = append(existingMap.Elements, &element)
					}
				case deleteVerb:
					key := substituteDefines(obj.Key, fake.defines)
					if i := findElement(existingMap.Elements, obj.Key); i != -1 {
						existingMap.Elements = append(existingMap.Elements[:i], existingMap.Elements[i+1:]...)
					} else {
						return notFoundError("no such element %q", key)
					}
				default:
					return fmt.Errorf("unhandled operation %q", op.verb)
				}
			}
		default:
			return fmt.Errorf("unhandled object type %T", op.obj)
		}
	}

	return nil
}

// Dump dumps the current contents of fake, in a way that looks like an nft transaction,
// but not actually guaranteed to be usable as such. (e.g., chains may be referenced
// before they are created, etc)
func (fake *Fake) Dump() string {
	if fake.Table == nil {
		return ""
	}

	buf := &strings.Builder{}

	table := fake.Table
	table.writeOperation(addVerb, fake.family, fake.table, buf)

	for _, cname := range sortKeys(table.Chains) {
		ch := table.Chains[cname]
		ch.writeOperation(addVerb, fake.family, fake.table, buf)

		for _, rule := range ch.Rules {
			// Avoid outputing handles
			dumpRule := *rule
			dumpRule.Handle = nil
			dumpRule.Index = nil
			dumpRule.writeOperation(addVerb, fake.family, fake.table, buf)
		}
	}

	for _, sname := range sortKeys(table.Sets) {
		s := table.Sets[sname]
		s.writeOperation(addVerb, fake.family, fake.table, buf)

		for _, element := range s.Elements {
			element.writeOperation(addVerb, fake.family, fake.table, buf)
		}
	}
	for _, mname := range sortKeys(table.Maps) {
		m := table.Maps[mname]
		m.writeOperation(addVerb, fake.family, fake.table, buf)

		for _, element := range m.Elements {
			element.writeOperation(addVerb, fake.family, fake.table, buf)
		}
	}

	return buf.String()
}

func sortKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func findRule(rules []*Rule, handle int) int {
	for i := range rules {
		if rules[i].Handle != nil && *rules[i].Handle == handle {
			return i
		}
	}
	return -1
}

func findElement(elements []*Element, key string) int {
	for i := range elements {
		if elements[i].Key == key {
			return i
		}
	}
	return -1
}

func (s *FakeSet) FindElement(key ...string) *Element {
	index := findElement(s.Elements, Join(key...))
	if index == -1 {
		return nil
	}
	return s.Elements[index]
}

func (m *FakeMap) FindElement(key ...string) *Element {
	index := findElement(m.Elements, Join(key...))
	if index == -1 {
		return nil
	}
	return m.Elements[index]
}
