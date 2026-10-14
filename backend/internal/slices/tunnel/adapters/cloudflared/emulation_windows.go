//go:build windows

package cloudflared

import (
	"debug/pe"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Ask Windows, rather than assuming every ARM64 installation supports x64.
// IsWow64GuestMachineSupported returns an HRESULT and a Win32 BOOL output.
func x64EmulationSupported() (bool, error) {
	probe := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsWow64GuestMachineSupported")
	if err := probe.Find(); err != nil {
		return false, err
	}
	var supported int32
	result, _, _ := probe.Call(uintptr(pe.IMAGE_FILE_MACHINE_AMD64), uintptr(unsafe.Pointer(&supported)))
	if result != 0 {
		return false, errors.New("Windows emulation support query failed")
	}
	return supported != 0, nil
}
