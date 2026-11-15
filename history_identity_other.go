//go:build !windows

package main

// Signature and publisher identity currently come from Windows Authenticode.
func identifyHistoryProcess(p HistoryProcess) HistoryProcess { return p }
