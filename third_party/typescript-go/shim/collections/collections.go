// Package collections re-exports the OrderedMap tsx needs to build the locked
// tsconfig `paths` map (specifier-pattern → target list). Pure re-export.
package collections

import "github.com/microsoft/typescript-go/internal/collections"

type (
	OrderedMap[K comparable, V any] = collections.OrderedMap[K, V]
	MapEntry[K comparable, V any]   = collections.MapEntry[K, V]
)

// NewOrderedMapFromList builds an OrderedMap from an ordered entry list.
func NewOrderedMapFromList[K comparable, V any](items []MapEntry[K, V]) *OrderedMap[K, V] {
	return collections.NewOrderedMapFromList(items)
}
