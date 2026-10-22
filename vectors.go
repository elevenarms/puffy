package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Vector search over a knowledge base table ("docs" or "pages").
//
// A knowledge base built with a quantization (meta "quantization", e.g.
// TURBO4) is searched in two steps: vector_quantize_scan returns
// meta "candidates" rows from the compact index, then puffy re-ranks them
// by exact cosine on the stored full vectors (FLOAT16). kb/eval_quant.py
// measured this at 99.9% of the exact top 40 with the same top 10, while
// the index is 1/6 the size of float32 vectors. Without a quantization it
// falls back to vector_full_scan.

// nearest returns rowids of the table's closest vectors, best first,
// keeping only rows of the given kind ("" for all).
func (kb *KnowledgeBase) nearest(table string, query []float32, kind string,
	limit int) ([]int64, error) {
	key := map[string]string{"docs": "id", "pages": "rowid"}[table]
	packed := packVector(query, kb.vectorType)
	if kb.candidates == 0 {
		// Scan deeper than limit so the kind filter still leaves limit.
		ids, err := kb.ids(fmt.Sprintf(`SELECT v.rowid FROM
			vector_full_scan('%s', 'embedding', ?, ?) AS v
			JOIN %s t ON t.%s = v.rowid WHERE (? = '' OR t.kind = ?)`,
			table, table, key), packed, limit*3, kind, kind)
		return ids[:min(limit, len(ids))], err
	}

	rows, err := kb.db.Query(fmt.Sprintf(`SELECT t.%s, t.embedding FROM
		vector_quantize_scan('%s', 'embedding', ?, ?) AS v
		JOIN %s t ON t.%s = v.rowid WHERE (? = '' OR t.kind = ?)`,
		key, table, table, key),
		packed, max(kb.candidates, limit*2), kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		id    int64
		score float64
	}
	var list []scored
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		list = append(list, scored{id, dot(query, blob, kb.vectorType)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	ids := make([]int64, 0, limit)
	for _, item := range list[:min(limit, len(list))] {
		ids = append(ids, item.id)
	}
	return ids, rows.Err()
}

// dot is the cosine of a unit query and a stored unit vector.
func dot(query []float32, blob []byte, vectorType string) float64 {
	var sum float64
	if vectorType == "FLOAT16" {
		for i := 0; i < len(query) && 2*i+1 < len(blob); i++ {
			half := binary.LittleEndian.Uint16(blob[2*i:])
			sum += float64(query[i]) * float64(halfToFloat(half))
		}
		return sum
	}
	for i := 0; i < len(query) && 4*i+3 < len(blob); i++ {
		bits := binary.LittleEndian.Uint32(blob[4*i:])
		sum += float64(query[i]) * float64(math.Float32frombits(bits))
	}
	return sum
}

// packVector encodes a query the way the column stores vectors; sqlite-
// vector requires the query blob to match the column type.
func packVector(vector []float32, vectorType string) []byte {
	if strings.EqualFold(vectorType, "FLOAT16") {
		buffer := make([]byte, 2*len(vector))
		for i, x := range vector {
			binary.LittleEndian.PutUint16(buffer[2*i:], floatToHalf(x))
		}
		return buffer
	}
	buffer := make([]byte, 4*len(vector))
	for i, x := range vector {
		binary.LittleEndian.PutUint32(buffer[4*i:], math.Float32bits(x))
	}
	return buffer
}

// halfToFloat decodes an IEEE 754 binary16 value.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exponent := uint32(h>>10) & 0x1f
	mantissa := uint32(h) & 0x3ff
	switch {
	case exponent == 0 && mantissa == 0:
		return math.Float32frombits(sign)
	case exponent == 0: // subnormal
		for mantissa&0x400 == 0 {
			mantissa <<= 1
			exponent--
		}
		exponent++
		mantissa &= 0x3ff
	case exponent == 0x1f: // inf or NaN
		return math.Float32frombits(sign | 0x7f800000 | mantissa<<13)
	}
	return math.Float32frombits(sign | (exponent+112)<<23 | mantissa<<13)
}

// floatToHalf encodes binary16 with round-to-nearest-even. Embedding
// values are in [-1, 1], so overflow only matters for completeness.
func floatToHalf(f float32) uint16 {
	bits := math.Float32bits(f)
	sign := uint16(bits>>16) & 0x8000
	exponent := int32(bits>>23&0xff) - 127 + 15
	mantissa := bits & 0x7fffff
	switch {
	case exponent >= 0x1f:
		if bits&0x7fffffff > 0x7f800000 {
			return sign | 0x7e00 // NaN
		}
		return sign | 0x7c00
	case exponent <= 0:
		if exponent < -10 {
			return sign
		}
		mantissa |= 0x800000
		shift := uint32(14 - exponent)
		half := mantissa >> shift
		rest := mantissa & (1<<shift - 1)
		midpoint := uint32(1) << (shift - 1)
		if rest > midpoint || (rest == midpoint && half&1 == 1) {
			half++
		}
		return sign | uint16(half)
	}
	half := uint32(exponent)<<10 | mantissa>>13
	rest := mantissa & 0x1fff
	if rest > 0x1000 || (rest == 0x1000 && half&1 == 1) {
		half++ // may carry into the exponent, which is correct
	}
	return sign | uint16(half)
}
