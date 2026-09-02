// Package parity holds ticfac's readers for the pinned ticks contract bundle:
// one per file, fourteen in all.
//
// contracts/README.md states the rule these exist to serve — "every
// implementation of a rule is pinned to one file, so a rule changed in one of
// them and not the others fails a test", and "a fixture with only one reader
// detects nothing". ticfac is the second (or third) reader for a bundle whose
// first readers are in ticks and in the factory's vitest suite.
//
// What a reader here can assert falls into three kinds, and each file's reader
// says which kind it is and why:
//
//   - EXECUTABLE. The fixture's cases are runnable from the fixture alone —
//     a regexp and its parse cases, a composition rule and its expected
//     strings, a schema and the documents it must admit and refuse, a state
//     machine and its sequences. These are full parity readers: ticfac
//     implements the rule and the fixture decides whether it agrees.
//
//   - CROSS-FILE. The bundle's own rule, applied across two contracts: a
//     record two files describe is defined by one of them (bundle 2.0.0), and
//     a value one file names is checked against the file that owns it.
//
//   - STRUCTURAL. The fixture pins a rule whose input is a format ticfac does
//     not parse yet (`.tick/runners.toml`), so its cases cannot be executed
//     here. The reader decodes the file into typed Go and asserts the shape,
//     the closure of its vocabularies and the invariants ticfac's later code
//     will depend on — so a fixture that changes shape under ticfac fails a
//     build here rather than at the moment the code that needed it is written.
//     Each such reader names the tick that will turn it executable.
//
// A structural reader is deliberately not called a parity reader. Calling it
// one would be the failure contracts/README.md warns about: a check that reads
// as if it asserted something while asserting nothing.
package parity
