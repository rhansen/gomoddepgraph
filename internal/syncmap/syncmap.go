package syncmap

import "sync"

type syncMap = sync.Map

type Map[K comparable, V any] struct {
	syncMap
}

func (m *Map[K, V]) LoadOrStore(k K, v V) (V, bool) {
	vAny, loaded := m.syncMap.LoadOrStore(k, v)
	return vAny.(V), loaded
}

func (m *Map[K, V]) Load(k K) (V, bool) {
	vAny, ok := m.syncMap.Load(k)
	if !ok {
		vAny = *new(V)
	}
	return vAny.(V), ok
}

func (m *Map[K, V]) Range(f func(K, V) bool) {
	m.syncMap.Range(func(k, v any) bool { return f(k.(K), v.(V)) })
}

func (m *Map[K, V]) ToMap() map[K]V {
	ret := map[K]V{}
	m.Range(func(k K, v V) bool {
		ret[k] = v
		return true
	})
	return ret
}

func (m *Map[K, V]) Delete(k K) {
	m.syncMap.Delete(k)
}

func (m *Map[K, V]) Swap(k K, v V) (V, bool) {
	vAny, ok := m.syncMap.Swap(k, v)
	if !ok {
		vAny = *new(V)
	}
	return vAny.(V), ok
}

func (m *Map[K, V]) CompareAndSwap(k K, vOld, vNew V) bool {
	return m.syncMap.CompareAndSwap(k, vOld, vNew)
}
