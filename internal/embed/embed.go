// Package embed provides the local-embedding arm of the hybrid funnel: an
// Ollama-backed Embedder plus the f32 vector codec and similarity helper.
// Hot-path package: stdlib only. The Embedder interface is structurally
// identical to gopheragent's tools.Embedder so a future upstream Ollama
// embedder can replace ollama.go without touching consumers (plan §upstream).
package embed

import (
	"context"
	"encoding/binary"
	"math"
)

// Embedder produces fixed-size vector representations of text.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// EncodeVec serializes a vector as little-endian float32 bytes — the
// `embeddings.vec` BLOB format.
func EncodeVec(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(f))
	}
	return out
}

// DecodeVec deserializes an EncodeVec blob. Returns nil for malformed input
// (length not a multiple of 4) — a stale/corrupt row degrades to "no vector",
// never an error on the hot path.
func DecodeVec(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

// Normalize scales v to unit length in place and returns it, so Dot below is
// cosine similarity. A zero vector is returned unchanged.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return v
	}
	inv := 1 / math.Sqrt(sum)
	for i := range v {
		v[i] = float32(float64(v[i]) * inv)
	}
	return v
}

// Dot returns the dot product — cosine similarity for Normalized vectors.
// Mismatched or empty vectors score 0 (treated as "no signal").
func Dot(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
