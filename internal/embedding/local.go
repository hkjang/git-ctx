package embedding

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"math"
	"regexp"
	"strings"
)

type Provider interface {
	Embed(context.Context, string) ([]float32, error)
}
type Metadata struct {
	Provider, Model, Revision string
	Dimensions                int
}
type MetadataProvider interface {
	Provider
	EmbeddingMetadata() Metadata
}
type Local struct{}

func (Local) Embed(_ context.Context, text string) ([]float32, error) { return Embed(text), nil }
func (Local) EmbeddingMetadata() Metadata {
	return Metadata{Provider: "local-feature-hash", Model: "fnv-token-projection", Revision: "v1", Dimensions: Dimensions}
}

const Dimensions = 256

var tokenRE = regexp.MustCompile(`[\pL\pN_./:-]+`)

func Tokens(text string) []string {
	raw := tokenRE.FindAllString(strings.ToLower(text), -1)
	out := make([]string, 0, len(raw))
	for _, token := range raw {
		if len([]rune(token)) > 1 {
			out = append(out, token)
		}
	}
	return out
}
func Embed(text string) []float32 {
	v := make([]float32, Dimensions)
	for _, token := range Tokens(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(token))
		sum := h.Sum64()
		index := sum % Dimensions
		sign := float32(1)
		if sum&(1<<63) != 0 {
			sign = -1
		}
		v[index] += sign
	}
	normalize(v)
	return v
}
func Encode(v []float32) []byte {
	raw := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(x))
	}
	return raw
}
func Decode(raw []byte) []float32 {
	if len(raw)%4 != 0 {
		return nil
	}
	v := make([]float32, len(raw)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return v
}
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i] * b[i])
	}
	return dot
}
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x * x)
	}
	if sum == 0 {
		return
	}
	n := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= n
	}
}
