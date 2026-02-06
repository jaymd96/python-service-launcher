package launchlib

// RLIMIT_NPROC is not exported by syscall on darwin in Go 1.25+.
// The raw value is 7 on macOS (same as RLIMIT_NPROC in sys/resource.h).
const rlimitNproc = 7
