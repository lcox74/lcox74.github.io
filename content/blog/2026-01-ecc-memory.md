---
title: "How ECC Memory Works"
date: 2026-01-11
tags: [hardware, golang, reliability, memory]
summary: "I always thought ECC was just error correction. Turns out the detection part is the whole point."
---

I've always heard that you should use ECC memory for servers and assumed it
was to error correct volatile memory and have some form of deterministic 
memory. None of my homelab machines have ECC memory installed and I've never
had a problem, and in this current economy where RAM prices are 5-10x higher 
than they were a few months ago I want to see if I can induce some delayed 
buyers remorse.

So that's what I'm going to do, sit down and actually see what the fuss is about
and should I actually think about upgrading my memory.

> The full working Go implementation is available at [ecc_mem.go](/code/ecc_mem.go).
> You can run it with `go run ecc_mem.go` to see the demo output.

## What is ECC Actually?

Random Access Memory (RAM) is super fast storage for active processes that run
on a machine. Typically the data stored in this memory is short-lived though
this isn't always the case and some data can be there for a long time. Data in
RAM can be corrupted by the environment it is in causing some bits to be 
flipped from `0` to `1` and vice versa. This can be catastrophic for certain
use cases like mission-critical systems, finances, virtualisation, databases and 
applications where data accuracy is critical. 

ECC is used to ensure data integrity and stability by storing some additional
parity bits to verify the data. It is only capable of correcting a single-bit
error/flip in a 64 bit word, any more errors are identified and are alerted to
the system loudly. Why is this important? Well the key to have stability is to
fail fast and fail loud, this way the system processing the data knows that 
there is a problem and will purposely crash or handle the error accordingly 
instead of putting itself into some unknown state.

All this is done on a hardware level, so it is very fast. Though it is 
important to note that it does add latency because of this. It also has an 
additional memory module to store the parity bits for each word. 

## Enter SECDED

The algorithm behind ECC memory is called SECDED: Single Error Correction,
Double Error Detection. The name tells you exactly what it does:

- **Single Error Correction**: If exactly one bit flips, we can identify which
  bit it was and flip it back
- **Double Error Detection**: If two bits flip, we can't fix it, but we know
  something is wrong

That second part is the key insight. Without ECC, a double bit flip just gives
you wrong data and you have no idea. With ECC, you get an alert that says "this
memory is corrupted, don't trust it." Your system can halt, log the error,
trigger an alert, or take whatever action is appropriate for the situation.

## How SECDED Actually Works

SECDED uses Hamming codes with an additional overall parity bit. For a 64-bit
data word, we add 8 bits of ECC metadata:

- 7 Hamming parity bits for error location
- 1 overall parity bit for double-error detection

The trick is in how we calculate those parity bits. Each Hamming bit covers a
specific subset of the data bits, chosen so that the pattern of which parity
bits fail uniquely identifies which data bit flipped.

```go
// ECCWord models an ECC-Protected memory word as:
//   - 64 bits of data
//   - 8 bits of ECC metadata
//
// In reality it would be stored as a 72 bit word (9 bytes)
type ECCWord struct {
	Data uint64
	ECC  uint8
}
```

So instead of storing 64 bits, we store 72 bits. That's where the "ECC requires
an extra memory module" thing comes from. Standard DIMMs have 8 chips for 64
bits. ECC DIMMs have 9 chips for 72 bits.

## Computing the ECC Bits

Each of the 7 Hamming parity bits is computed by XORing together a specific
subset of data bits. The subsets are defined using bitmasks:

```go
// eccMasks contains precomputed masks for Hamming parity calculation.
// Uses standard Hamming encoding where parity bits occupy power-of-2 positions
// (1, 2, 4, 8, 16, 32, 64) and data bits occupy the remaining positions.
var eccMasks = [7]uint64{
	0xAB55555556AAAD5B, // P0
	0xCD9999999B33366D, // P1
	0xF1E1E1E1E3C3C78E, // P2
	0x01FE01FE03FC07F0, // P3
	0x01FFFE0003FFF800, // P4
	0x01FFFFFFFC000000, // P5
	0xFE00000000000000, // P6
}
```

I assume the controller on the boards have a predefined mapping like this to
quickly calculate each ECC metadata parity bit. Though I don't actually know.
These masks are a precalculated Hamming encoding, which interleaves parity bits
at power-of-2 positions (1, 2, 4, 8, 16, 32, 64) with data bits filling the
gaps. Each parity bit covers positions where a specific bit in the position
number is set. For example, P0 covers all positions where bit 0 is set (1, 3,
5, 7, 9, ...), P1 covers positions where bit 1 is set (2, 3, 6, 7, 10, 11, ...),
and so on. The actual computation is straightforward:

```go
// computeECC calculates the 8 ECC bits for a given 64-bit data word.
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
```

For each parity bit, we AND the data with its mask and count the `1`s. If
there's an odd number, that parity bit is `1`. The 8th bit is the overall
parity, making the entire 72-bit word have an even number of `1`s.

## Reading and Detecting Errors

When reading, we recompute what the ECC should be and XOR it with what we
stored. This XOR is called the syndrome:

```go
expectedECC := computeECC(w.Data)
syndrome := w.ECC ^ expectedECC
```

If nothing flipped, the syndrome is zero. If something changed, the syndrome
tells us exactly what went wrong. We split into the Hamming syndrome (lower
7 bits) and check the overall parity separately:

```go
hammingSyndrome := syndrome & 0x7F

// Check overall parity of the entire stored word (data + ECC)
totalOnes := bits.OnesCount64(w.Data) + bits.OnesCount8(w.ECC)
parityError := totalOnes%2 != 0
```

## Correcting Single-Bit Errors

When we detect a single-bit error (non-zero syndrome, odd parity), the syndrome
directly tells us the Hamming position of the flipped bit. But there's a subtle
detail: power-of-2 syndromes indicate a parity bit error, not a data bit error.

```go
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
```

The `hammingToData` lookup table maps from the Hamming position (what the
syndrome gives us) to the actual data bit position. Positions 1, 2, 4, 8, 16,
32, 64 are parity bits, so they map to `-1`.

```go
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
```

I generated this lookup table once and hardcoded the result for speed. I assume
this is another thing that the manufacturers do on the controller level.

## Demo Time

Let's see it work. I wrote a test harness that creates words, corrupts them,
then reads them back:

```go
tests := []testCase{
	// No error
	{"Clean read", 0xDEADBEEFCAFEBABE, 0, 0},

	// Single-bit data errors
	{"Single-bit data error (bit 0)", 0xDEADBEEFCAFEBABE, 0x01, 0},
	{"Single-bit data error (bit 63)", 0xDEADBEEFCAFEBABE, 1 << 63, 0},

	// Single-bit ECC errors
	{"Single-bit ECC error (P0)", 0xDEADBEEFCAFEBABE, 0, 0x01},
	{"Single-bit ECC error (overall parity)", 0xDEADBEEFCAFEBABE, 0, 0x80},

	// Multi-bit errors (uncorrectable)
	{"Multi-bit data error", 0xDEADBEEFCAFEBABE, 0x05, 0},
}
```

Running this:

```text
[Clean read]
	Original: ECCWord data=0xDEADBEEFCAFEBABE ecc=11001010
	Corrupted: ECCWord data=0xDEADBEEFCAFEBABE ecc=11001010

	Recovered: ECCWord data=0xDEADBEEFCAFEBABE ecc=11001010
	Status: OK

[Single-bit data error (bit 0)]
	Original: ECCWord data=0xDEADBEEFCAFEBABE ecc=11001010
	Corrupted: ECCWord data=0xDEADBEEFCAFEBABF ecc=11001010

	Recovered: ECCWord data=0xDEADBEEFCAFEBABE ecc=11001010
	Status: Corrected single-bit data error

[Multi-bit data error]
	Original: ECCWord data=0xDEADBEEFCAFEBABE ecc=11001010
	Corrupted: ECCWord data=0xDEADBEEFCAFEBABB ecc=11001010

	Recovered: <invalid>
	Error: ecc: uncorrectable multi-bit error
```

The single-bit error gets corrected silently. The multi-bit error triggers an
error that the system can handle appropriately.

## The Hardware Reality

In my Go code, all this computation happens in software. Real ECC memory does
this in hardware using dedicated ECC controller chips on the memory controller.
The algorithm is the same, but it runs at near memory speeds.

There are trade-offs:

- **Latency**: Those extra calculations add a few nanoseconds per access. For
  most server workloads this is negligible, but it matters for latency-critical
  applications
- **Cost**: That 9th chip per DIMM, the ECC controller, and the need for server-
  grade motherboards that support it all add up

The reason servers use ECC despite these costs is simple: silent data corruption
is catastrophic for databases, filesystems, and long-running processes. A few
nanoseconds of latency is nothing compared to discovering your backup has been
silently corrupted for months.

## When SECDED Isn't Enough

SECDED handles single-bit errors well and detects double-bit errors. But what
about three bits? Or an entire chip failure?

Modern servers use more sophisticated schemes:

- **Chipkill/SDDC** (Single Device Data Correction): Can recover from an entire
  memory chip failing, not just a single bit
- **DDDC** (Double Device Data Correction): Can handle two chip failures
- **Memory mirroring**: Complete redundancy at 50% capacity cost

These are for mission-critical systems where even the fail-fast approach isn't
good enough. You want to survive the failure entirely while you hot-swap the
failing DIMM.

## So, Should I Upgrade?

Honestly? Probably not. My homelab runs Plex, some game servers, and the
occasional container I forget about. If a bit flips and my Minecraft world
gets corrupted, I'll just restore from backup and blame cosmic rays at the
pub. 

The real value of ECC isn't the correction, it's the detection. Knowing that
your data is wrong is infinitely more valuable than silently propagating
garbage. For servers handling anything actually important, that peace of mind
is worth the premium. For my homelab? I'll take the risk and spend the
difference on more storage I don't need.

At least now I understand what I'm missing out on. Sometimes the best outcome
of research is a well-informed decision to do absolutely nothing.

