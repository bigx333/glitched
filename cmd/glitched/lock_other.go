//go:build !unix

package main

func lock(string) error { return nil }
