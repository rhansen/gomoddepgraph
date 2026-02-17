package itertools

import (
	"fmt"
	"iter"

	"golang.org/x/exp/constraints"
)

func Cat[T any](seqs ...iter.Seq[T]) iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, seq := range seqs {
			for v := range seq {
				if !yield(v) {
					return
				}
			}
		}
	}
}

func Cat2[K, V any](seqs ...iter.Seq2[K, V]) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for _, seq := range seqs {
			for k, v := range seq {
				if !yield(k, v) {
					return
				}
			}
		}
	}
}

func Attach[K, V any](seq iter.Seq[K], v V) iter.Seq2[K, V] {
	return Map12(seq, func(k K) (K, V) { return k, v })
}

func Filter[T any](seq iter.Seq[T], pred func(T) bool) iter.Seq[T] {
	return func(yield func(T) bool) {
		for v := range seq {
			if pred(v) && !yield(v) {
				return
			}
		}
	}
}

func Filter2[K, V any](seq iter.Seq2[K, V], pred func(K, V) bool) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for k, v := range seq {
			if pred(k, v) && !yield(k, v) {
				return
			}
		}
	}
}

func First[K, V any](seq iter.Seq2[K, V]) iter.Seq[K] {
	return Map21(seq, func(k K, _ V) K { return k })
}

func Swap[K, V any](seq iter.Seq2[K, V]) iter.Seq2[V, K] {
	return Map2(seq, func(k K, v V) (V, K) { return v, k })
}

func Map[Vin, Vout any](seq iter.Seq[Vin], transform func(Vin) Vout) iter.Seq[Vout] {
	return func(yield func(Vout) bool) {
		for v := range seq {
			if !yield(transform(v)) {
				return
			}
		}
	}
}

func Map12[Vin, Kout, Vout any](seq iter.Seq[Vin], transform func(Vin) (Kout, Vout)) iter.Seq2[Kout, Vout] {
	return func(yield func(Kout, Vout) bool) {
		for v := range seq {
			if !yield(transform(v)) {
				return
			}
		}
	}
}

func Map2[Kin, Vin, Kout, Vout any](seq iter.Seq2[Kin, Vin], transform func(Kin, Vin) (Kout, Vout)) iter.Seq2[Kout, Vout] {
	return func(yield func(Kout, Vout) bool) {
		for k, v := range seq {
			if !yield(transform(k, v)) {
				return
			}
		}
	}
}

func Map21[Kin, Vin, Vout any](seq iter.Seq2[Kin, Vin], transform func(Kin, Vin) Vout) iter.Seq[Vout] {
	return func(yield func(Vout) bool) {
		for k, v := range seq {
			if !yield(transform(k, v)) {
				return
			}
		}
	}
}

func Range[Int constraints.Unsigned](start, end Int) iter.Seq[Int] {
	return func(yield func(Int) bool) {
		for i := start; i < end; i++ {
			if !yield(i) {
				return
			}
		}
	}
}

func Stringify[V fmt.Stringer](seq iter.Seq[V]) iter.Seq[string] {
	return Map(seq, func(v V) string { return v.String() })
}
