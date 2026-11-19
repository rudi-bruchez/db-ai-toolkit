//go:build !windows

package main

import "syscall"

// The transport errors that mean "cut" and "refused", as this platform spells them.
var (
	resetErrnos   = []error{syscall.ECONNRESET, syscall.ECONNABORTED}
	refusedErrnos = []error{syscall.ECONNREFUSED}
)
