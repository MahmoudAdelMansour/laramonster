package php

import "golang.org/x/sys/windows"

// Child processes inherit the error mode: without this, a php-cgi.exe that
// can't find a DLL (no Visual C++ runtime) waits on a system dialog instead
// of exiting with STATUS_DLL_NOT_FOUND, which FindBroken explains.
func init() {
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS | windows.SEM_NOOPENFILEERRORBOX)
}
