//go:build darwin && amd64

package sysinfo

import (
	"math"
	"testing"
)

func TestDecodeSMCTemp(t *testing.T) {
	sp78 := fourCC("sp78")
	flt := fourCC("flt ")

	cases := []struct {
		name     string
		dataType uint32
		dataSize uint32
		bytes    []byte
		want     float64
	}{
		// sp78 : point fixe signé 8.8, grand-boutiste. 0x4e = 78, 0xe0 = 224/256.
		{"sp78 nominal", sp78, 2, []byte{0x4e, 0xe0}, 78.875},
		{"sp78 entier", sp78, 2, []byte{0x32, 0x00}, 50},
		// flt : float32 petit-boutiste. 42.5 == 0x42250000.
		{"flt nominal", flt, 4, littleEndian32(math.Float32bits(42.5)), 42.5},
		// Formats/tailles inattendus → 0 (et non une valeur parasite).
		{"type inconnu", fourCC("ui8 "), 1, []byte{0x40}, 0},
		{"sp78 tronqué", sp78, 1, []byte{0x40}, 0},
	}

	for _, c := range cases {
		b := make([]byte, 32)
		copy(b, c.bytes)
		if got := decodeSMCTemp(c.dataType, c.dataSize, b); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s : decodeSMCTemp = %v, attendu %v", c.name, got, c.want)
		}
	}
}

func littleEndian32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
