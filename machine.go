package main

// machine is one computer the app works with. Each has its own scan, folder views,
// searches and monitor, so work on one never disturbs another. The page names a machine
// by its target: "local" for this computer, "remote" for the SSH server.
type machine struct {
	target  string
	local   bool
	scan    scanSession
	view    job[ViewStatus]
	search  searchSession
	monitor monitorSession
}

var (
	localMachine  = &machine{target: "local", local: true}
	remoteMachine = &machine{target: "remote"}
	machines      = []*machine{localMachine, remoteMachine}
)

// machineFor returns the machine the page calls target. Anything unknown is this computer.
func machineFor(target string) *machine {
	for _, m := range machines {
		if m.target == target {
			return m
		}
	}
	return localMachine
}

// machineOf returns the machine a connection leads to.
func machineOf(c Connection) *machine {
	if c.Local {
		return localMachine
	}
	return remoteMachine
}
