package client

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// startInOwnConsole starts argv (a console program: agentnet open) in a new
// console window of its own, in dir, with that console as its standard
// input and output: CreateProcess with CREATE_NEW_CONSOLE and no
// STARTF_USESTDHANDLES, inheriting no handles. (os/exec always passes its
// own standard handles, pipes or the null device, which a new console does
// not replace, so an interactive session there could neither read nor
// write.) The command line is argv quoted by the documented rules, never a
// shell string.
func startInOwnConsole(argv []string, dir string) error {
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return err
	}
	var cwd *uint16
	if dir != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			return err
		}
	}
	si := windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, line, nil, nil, false, windows.CREATE_NEW_CONSOLE|windows.CREATE_UNICODE_ENVIRONMENT,
		nil, cwd, &si, &pi); err != nil {
		return err
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}
