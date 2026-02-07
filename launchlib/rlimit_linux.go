package launchlib

// RLIMIT_NPROC is not exported by syscall on linux in Go 1.25+.
// The raw value is 6 on Linux (same as RLIMIT_NPROC in bits/resource.h).
const rlimitNproc = 6
