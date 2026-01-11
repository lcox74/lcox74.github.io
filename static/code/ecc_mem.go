package main

import (
	"errors"
	"fmt"
	"math/bits"
)

// ErrUncorrectable is returned when a multi-bit error is detected that cannot
// be corrected by the SECDED algorithm.
var ErrUncorrectable = errors.New("ecc: uncorrectable multi-bit error")

// ReadStatus indicates the outcome of a Read operation.
type ReadStatus int

const (
	StatusOK            ReadStatus = iota // No errors detected
	StatusCorrectedData                   // Single-bit data error was corrected
	StatusCorrectedECC                    // Single-bit ECC metadata error was corrected
)

// String returns a human-readable description of the read status.
func (s ReadStatus) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusCorrectedData:
		return "Corrected single-bit data error"
	case StatusCorrectedECC:
		return "Corrected single-bit ECC error"
	default:
		return "Unknown status"
	}
}

// ReadResult contains the outcome of a successful Read operation.
type ReadResult struct {
	Data   uint64
	Status ReadStatus
}

// ECCWord models an ECC-Protected memory word as:
//   - 64 bits of data
//   - 8 bits of ECC metadata
//
// In reality it would be stored as a 72 bit word (9 bytes)
type ECCWord struct {
	Data uint64
	ECC  uint8
}

// String returns a human-readable representation of the ECCWord showing the
// 64-bit data in hexadecimal and the 8-bit ECC metadata in binary.
func (w ECCWord) String() string {
	return fmt.Sprintf(
		"ECCWord data=0x%016X ecc=%08b",
		w.Data, w.ECC,
	)
}

// Write simulates a memory write
func Write(data uint64) *ECCWord {
	return &ECCWord{
		Data: data,
		ECC:  computeECC(data),
	}
}

// Read simulates a memory read with error detection and correction.
// Returns a ReadResult on success (including corrected errors) or an error
// if the data is unrecoverable.
func Read(w *ECCWord) (ReadResult, error) {
	assert(w != nil, "require ECCWord to read")

	expectedECC := computeECC(w.Data)
	syndrome := w.ECC ^ expectedECC

	hammingSyndrome := syndrome & 0x7F

	// Check overall parity of the entire stored word (data + ECC)
	totalOnes := bits.OnesCount64(w.Data) + bits.OnesCount8(w.ECC)
	parityError := totalOnes%2 != 0

	// No error: hamming matches and parity is even
	if hammingSyndrome == 0 && !parityError {
		return ReadResult{Data: w.Data, Status: StatusOK}, nil
	}

	// Single-bit error: parity is odd and syndrome is non-zero
	if parityError && hammingSyndrome != 0 {
		syndrome := int(hammingSyndrome)

		// Power-of-2 syndromes indicate a Hamming parity bit error (not data)
		if isPowerOfTwo(syndrome) {
			w.ECC = computeECC(w.Data)
			return ReadResult{Data: w.Data, Status: StatusCorrectedECC}, nil
		}

		// Non-power-of-2 syndrome indicates a data bit error
		if syndrome < len(hammingToData) {
			dataBit := hammingToData[syndrome]
			if dataBit >= 0 && dataBit < 64 {
				w.Data ^= 1 << dataBit
				w.ECC = computeECC(w.Data)
				return ReadResult{Data: w.Data, Status: StatusCorrectedData}, nil
			}
		}
	}

	// Single-bit ECC error: parity is odd but syndrome is zero (overall parity bit error)
	if parityError && hammingSyndrome == 0 {
		w.ECC = computeECC(w.Data)
		return ReadResult{Data: w.Data, Status: StatusCorrectedECC}, nil
	}

	// Multi-bit error: parity is even but hamming is non-zero
	return ReadResult{}, ErrUncorrectable
}

// eccMasks contains precomputed masks for Hamming parity calculation.
// Uses standard Hamming encoding where parity bits occupy power-of-2 positions
// (1, 2, 4, 8, 16, 32, 64) and data bits occupy the remaining positions.
// This ensures power-of-2 syndromes always indicate ECC errors, not data errors.
var eccMasks = [7]uint64{
	0xAB55555556AAAD5B, // P0
	0xCD9999999B33366D, // P1
	0xF1E1E1E1E3C3C78E, // P2
	0x01FE01FE03FC07F0, // P3
	0x01FFFE0003FFF800, // P4
	0x01FFFFFFFC000000, // P5
	0xFE00000000000000, // P6
}

// hammingToData maps Hamming position back to data bit index (-1 for parity positions).
var hammingToData = [128]int{
	-1, -1, -1, 0, -1, 1, 2, 3, // 0-7
	-1, 4, 5, 6, 7, 8, 9, 10, // 8-15
	-1, 11, 12, 13, 14, 15, 16, 17, // 16-23
	18, 19, 20, 21, 22, 23, 24, 25, // 24-31
	-1, 26, 27, 28, 29, 30, 31, 32, // 32-39
	33, 34, 35, 36, 37, 38, 39, 40, // 40-47
	41, 42, 43, 44, 45, 46, 47, 48, // 48-55
	49, 50, 51, 52, 53, 54, 55, 56, // 56-63
	-1, 57, 58, 59, 60, 61, 62, 63, // 64-71
	-1, -1, -1, -1, -1, -1, -1, -1, // 72-79
	-1, -1, -1, -1, -1, -1, -1, -1, // 80-87
	-1, -1, -1, -1, -1, -1, -1, -1, // 88-95
	-1, -1, -1, -1, -1, -1, -1, -1, // 96-103
	-1, -1, -1, -1, -1, -1, -1, -1, // 104-111
	-1, -1, -1, -1, -1, -1, -1, -1, // 112-119
	-1, -1, -1, -1, -1, -1, -1, -1, // 120-127
}

// isPowerOfTwo returns true if n is a power of two.
func isPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
}

// computeECC calculates the 8 ECC bits for a given 64-bit data word. This
// implements the SECDED-style scheme of:
//   - 7 Hamming parity bits: locate a single flipped bit
//   - 1 overall parity bit: detect double-bit errors
func computeECC(data uint64) uint8 {
	var ecc uint8

	for i, mask := range eccMasks {
		if bits.OnesCount64(data&mask)%2 != 0 {
			ecc |= 1 << i
		}
	}

	// Overall parity makes total (data + all ECC bits) even
	if (bits.OnesCount64(data)+bits.OnesCount8(ecc))%2 != 0 {
		ecc |= 1 << 7
	}

	return ecc
}

func assert(condition bool, msg string) {
	if !condition {
		panic("assertion failed: " + msg)
	}
}

type testCase struct {
	name    string
	data    uint64
	dataXOR uint64 // bits to flip in data
	eccXOR  uint8  // bits to flip in ECC
}

func main() {
	tests := []testCase{
		// No error
		{"Clean read", 0xDEADBEEFCAFEBABE, 0, 0},

		// Single-bit data errors
		{"Single-bit data error (bit 0)", 0xDEADBEEFCAFEBABE, 0x01, 0},
		{"Single-bit data error (bit 2)", 0xDEADBEEFCAFEBABE, 0x04, 0},
		{"Single-bit data error (bit 63)", 0xDEADBEEFCAFEBABE, 1 << 63, 0},

		// Single-bit ECC errors (Hamming parity bits P0-P6)
		{"Single-bit ECC error (P0)", 0xDEADBEEFCAFEBABE, 0, 0x01},
		{"Single-bit ECC error (P1)", 0xDEADBEEFCAFEBABE, 0, 0x02},
		{"Single-bit ECC error (P2)", 0xDEADBEEFCAFEBABE, 0, 0x04},
		{"Single-bit ECC error (P6)", 0xDEADBEEFCAFEBABE, 0, 0x40},
		{"Single-bit ECC error (overall parity)", 0xDEADBEEFCAFEBABE, 0, 0x80},

		// Multi-bit errors (uncorrectable)
		{"Multi-bit data error", 0xDEADBEEFCAFEBABE, 0x05, 0},
		{"Multi-bit ECC error", 0xDEADBEEFCAFEBABE, 0, 0x03},
	}

	for _, tc := range tests {
		fmt.Printf("[%s]\n", tc.name)

		word := Write(tc.data)
		fmt.Printf("\tOriginal: %s\n", word)

		word.Data ^= tc.dataXOR
		word.ECC ^= tc.eccXOR
		fmt.Printf("\tCorrupted: %s\n", word)

		result, err := Read(word)
		if err != nil {
			fmt.Println("\n\tRecovered: <invalid>")
			fmt.Printf("\tError: %s\n", err)
		} else {
			fmt.Printf("\n\tRecovered: %s\n", word)
			fmt.Printf("\tStatus: %s\n", result.Status)
		}
		fmt.Println()
	}
}
