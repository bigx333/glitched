//go:build unix

package main

import "syscall"

// openFlags open a file to read without blocking on a FIFO or following a
// link in its last element.
const openFlags = syscall.O_NONBLOCK | syscall.O_NOFOLLOW
