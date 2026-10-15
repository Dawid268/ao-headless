package agentbrowser

import "syscall"

// dieWithWorker sends the service SIGTERM when the worker dies, even killed
// outright (the OOM killer): it then closes Chromium, instead of an orphan
// holding the profile while the next worker starts a second Chromium on it.
func dieWithWorker(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}
