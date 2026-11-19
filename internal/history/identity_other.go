//go:build !windows

package history

// Signature and publisher identity currently come from Windows Authenticode.
func identifyHistoryProcess(p Process) Process { return p }
