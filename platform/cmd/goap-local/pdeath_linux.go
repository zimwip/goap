package main

import "syscall"

// dieWithParent has the process ended when goap-local dies without stopping it (killed itself): no service is left
// holding its port for the next run. SIGTERM, so that it still closes its database.
func dieWithParent(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGTERM }
