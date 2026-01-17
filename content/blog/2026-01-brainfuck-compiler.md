---
title: "Building a Brainfuck Compiler"
date: 2026-01-17
tags: [golang, compilers, brainfuck, x86_64]
summary: "University had me 'build' a compiler by adding features to someone else's code. This time I wanted the segfaults to be my own."
---

I took a compilers course at university (COMP4403) where we built a compiler
for PL0. I say "built", but it was more like we added features to an existing
implementation the course provided. I passed the course but never felt like I
actually understood what was happening under the hood.

Since then I've built interpreters for various languages, including Brainfuck.
Interpreters are satisfying but they dodge the hard parts: you never have to
think about optimisation or code generation because you're just executing
as you go.

So I decided to build an actual ahead-of-time compiler. Brainfuck seemed like
the right target - the language is simple enough that I could focus on the
compiler infrastructure without getting lost in language semantics.

The result is [bfcc](https://github.com/lcox74/bfcc), a Brainfuck compiler
written in Go that outputs native Linux x86_64 executables. Note that it works,
not that it is good.

## The Language

Brainfuck operates on an infinite tape, but the standard practise everyone
seem to follow is having the length of the tape to be 30,000 cells, each 
holding a byte. You have a data pointer that starts at cell 0. The entire 
language is 8 characters:

| Character | Operation |
|-----------|-----------|
| `>` | Move pointer right |
| `<` | Move pointer left |
| `+` | Increment current cell |
| `-` | Decrement current cell |
| `.` | Output current cell as ASCII |
| `,` | Read byte into current cell |
| `[` | Jump past matching `]` if cell is zero |
| `]` | Jump back to matching `[` if cell is non-zero |

Everything else is a comment. This means you can write documentation inline
and it just gets ignored.

Hello World looks like this:

```brainfuck
++++++++[>++++[>++>+++>+++>+<<<<-]>+>+>->>+[<]<-]
>>.>---.+++++++..+++.>>.<-.<.+++.------.--------.>>+.>++.
```

Yeah. It's an esoteric language for a reason.

## The Pipeline

The compiler follows a classic structure:

```
Source -> Tokenizer -> Lower -> Optimizer -> Code Generator -> Executable
```

Each stage transforms the program into something closer to machine code.

## Tokenizing

The tokenizer reads the source and produces a stream of tokens, discarding
anything that isn't one of the 8 valid characters. Each token is simply a
kind and its position in the source:

```go
type Token struct {
    Kind TokenKind
    Pos  Position  // offset, line, column
}
```

No folding happens here, the tokenizer just identifies valid characters and
tracks where they appear for error reporting. I probably could do the folding
here but I dubbed it an optimisation so moved it to IR.

## Lowering to IR

Tokens get converted to an intermediate representation. This is where
consecutive identical tokens get folded together; `++++` becomes a single
`ADD +4` operation. The IR has a few more operation types than the source
language:

| IR Op | Description |
|-------|-------------|
| `SHIFT k` | Move pointer by k cells (positive or negative) |
| `ADD k` | Add k to current cell (positive or negative) |
| `ZERO` | Set current cell to 0 |
| `IN` | Read from stdin |
| `OUT` | Write to stdout |
| `JZ target` | Jump to target if cell is zero |
| `JNZ target` | Jump to target if cell is non-zero |

> **Note:** The `ZERO` operation doesn't exist in Brainfuck, it gets 
> introduced during optimisation.

Here's a concrete example. This Brainfuck multiplies cell 0 by 10 and stores
the result in cell 1:

```brainfuck
++++++[>++++++++++<-]>
```

In English: set cell 0 to 6, then loop (add 10 to cell 1, subtract 1 from
cell 0) until cell 0 is zero, then move to cell 1. Result: 60.

The IR looks like this:

```text
000: ADD +6
001: JZ 007
002: SHIFT +1
003: ADD +10
004: SHIFT -1
005: ADD -1
006: JNZ 001
007: SHIFT +1
```

The brackets become explicit jumps. `JZ 007` means "if current cell is zero,
jump to instruction 7". `JNZ 001` means "if current cell is not zero, jump
back to instruction 1". The loop body is just the operations between them.

## Optimisation

This was the part I'd never done before, and it turned out to be easier than
expected. At this level, optimisation is mostly pattern matching on the list
of operations. There are three optimisation levels, which I did for testing
and debugging:

**O0**: No optimisation. Just the raw IR from lowering.

**O1**: Fold adjacent operations, remove no-ops, and normalize values.

```
ADD +4, ADD -4  ->  ADD 0  ->  (removed)
SHIFT +2, SHIFT -2  ->  SHIFT 0  ->  (removed)
ADD +300  ->  ADD +44  (normalized to 8-bit range)
```

**O2**: Detect zeroing loops and remove empty loops.

The pattern `[-]` (or `[+]`) decrements the current cell until it hits zero.
This is a common idiom for clearing a cell. Instead of looping, we can replace
it with a single `ZERO` operation.

```
JZ +2, ADD -1, JNZ -1  ->  ZERO
```

Empty loops `[]` are also removed entirely. These are sometimes used as
comments in Brainfuck since the loop body is skipped if the cell is already
zero at the start of the program.

The implementation is just walking the IR list, checking for patterns, and
either merging operations or removing them entirely. Nothing clever, but it
works. Until you realised you have modified the IR instruction set without
fixing the jump addresses and then completely break the program.

## Code Generation

This is where things got interesting. I started by targeting GAS (GNU
Assembler) because I could read the output, debug it and suffer using gdb.

The memory model is simple:
- `%r13` holds the base address of the tape (30,000 bytes in BSS)
- `%r12` holds the current offset (data pointer position)
- Current cell is accessed via `(%r13,%r12)`, base plus offset

Each IR operation maps to a small sequence of instructions:

```asm
# SHIFT +3
addq $3, %r12

# ADD +5
addb $5, (%r13,%r12)

# ZERO
movb $0, (%r13,%r12)

# JZ (jump if zero)
testb $0xff, (%r13,%r12)
jz .target

# JNZ (jump if not zero)
testb $0xff, (%r13,%r12)
jnz .target
```

I/O uses Linux syscalls. Initially I inlined the full syscall sequence
everywhere, but the output was massive. Instead, I emit helper functions once
at the end and just `call` them:

```asm
_bf_write:
    leaq (%r13,%r12), %rsi   # buffer = current cell address
    movq $1, %rax            # syscall: write
    movq $1, %rdi            # fd: stdout
    movq $1, %rdx            # count: 1 byte
    syscall
    ret
```

Getting here involved a lot of segfaults. Turns out using the wrong registers
for syscalls produces exciting results. The x86_64 calling convention is
particular about which registers hold which arguments, and I learned this
the hard way.

## Going Native

Once the GAS output was working, I wanted to skip the external assembler
entirely and emit ELF executables directly. This turned out to be two separate
problems: encoding x86_64 instructions, and building the ELF binary format.

### x86_64 Instruction Encoding

This was harder than expected. x86_64 instruction encoding is... baroque. The
same logical operation can have multiple encodings depending on operand sizes,
register choices, and addressing modes. The osdev wiki  are comprehensive
but dense.

I started by manually encoding instructions based on the wiki documentation,
but eventually got frustrated and used Claude Code to generate an `amd64`
package with the encodings I needed. I went back and added comments linking
each encoding to the relevant wiki documentation so future-me would understand
what was happening.

The result is a set of functions that emit bytes for each instruction type:

```go
// addq $imm, %r12 - add immediate to r12
func AddqImmR12(imm int32) []byte

// movb $0, (%r13,%r12) - move zero to memory at r13+r12
func MovbZeroR13R12() []byte
```

### ELF Format

The ELF part was simpler, though I still had to figure out what was going on.
My first approach was compiling a minimal GAS program and hexdumping the
output to reverse engineer the header byte by byte. This worked up to a point,
but I kept getting confused about which bytes were fixed and which were
calculated. Eventually I gave up on the forensic approach and just followed
some ELF format documentation online and realised I was a bit thick.

A minimal static executable needs:

1. An ELF header (fixed 64 bytes)
2. A program header (tells the kernel how to load the binary)
3. The actual code
4. BSS section for the tape (just needs to be declared, not stored)

The header has some magic numbers and then sizes/offsets that you calculate
based on your code size. Nothing clever, just following the spec.

```go
type ELF struct {
    Code     []byte  // machine code
    BSSSize  uint64  // size of uninitialized data
}
```

The code gets loaded at `0x400000` (standard Linux base address) and BSS at
`0x600000`. The entry point is right after the headers.

## The Result

```bash
$ bfcc build -o hello testdata/helloworld.bf
$ ./hello
Hello World!
$ ls -la hello
-rwxr-xr-x@ 1 lcox74  staff  4567 18 Jan 08:48 hello
```

A 4KB standalone executable with no dependencies. The VM interpreter is useful
for testing, and the GAS output is useful for debugging, but there's something
satisfying about emitting raw bytes that the kernel can execute directly.

## What's Next

The compiler works but there's plenty more to explore:

- **More optimisations**: There are well-known Brainfuck optimisations I
  haven't implemented, like scan loops (`[>]`) and multiply loops
- **Other targets**: ARM64 would be interesting, or even WASM
- **Better error messages**: Currently bracket mismatches just panic

But honestly, the goal was to understand how compilers work, not to build the
world's best Brainfuck compiler. Mission accomplished. The next time I look at
a compiler codebase, I'll actually know what the stages are doing.

The code is at [github.com/lcox74/bfcc](https://github.com/lcox74/bfcc) if
you want to see the implementation or try it yourself.
