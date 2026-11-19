//go:build windows

package main

import "syscall"

// On Windows a socket error is a WSA code: syscall.ECONNRESET is an invented
// value that errors.Is never matches against a read error, which carries
// WSAECONNRESET (10054). The numbers are those of winsock2.h.
var (
	resetErrnos   = []error{syscall.ECONNRESET, syscall.Errno(10054), syscall.Errno(10053)}
	refusedErrnos = []error{syscall.ECONNREFUSED, syscall.Errno(10061)}
)
