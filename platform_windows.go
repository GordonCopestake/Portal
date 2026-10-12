package main

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Only enable colour on a Windows console. Pipes and files stay plain text.
func enableColour(out io.Writer) (bool, func()) {
	file, ok := out.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false, func() {}
	}
	handle := windows.Handle(file.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil || windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.ENABLE_PROCESSED_OUTPUT) != nil {
		return false, func() {}
	}
	return true, func() { windows.SetConsoleMode(handle, mode) }
}

func isNetworkPath(path string) bool {
	normalized := strings.ReplaceAll(path, "/", "\\")
	if strings.HasPrefix(normalized, "\\\\") {
		if !strings.HasPrefix(normalized, "\\\\?\\") {
			return true
		}
		if strings.HasPrefix(strings.ToUpper(normalized), "\\\\?\\UNC\\") {
			return true
		}
	}
	volume := filepath.VolumeName(normalized)
	if volume == "" {
		return false
	}
	root, err := syscall.UTF16PtrFromString(volume + "\\")
	if err != nil {
		return false
	}
	typeID, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW").Call(uintptr(unsafe.Pointer(root)))
	return typeID == 4 // DRIVE_REMOTE, including mapped network drives.
}

func workstationIdentity() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	account, err := user.Current()
	if err != nil {
		return "", err
	}
	advapi := syscall.NewLazyDLL("advapi32.dll")
	var token syscall.Handle
	ok, _, callErr := advapi.NewProc("OpenProcessToken").Call(^uintptr(0), 0x0008, uintptr(unsafe.Pointer(&token)))
	if ok == 0 {
		return "", fmt.Errorf("OpenProcessToken: %w", callErr)
	}
	defer syscall.CloseHandle(token)
	var elevated, returned uint32
	ok, _, callErr = advapi.NewProc("GetTokenInformation").Call(uintptr(token), 20,
		uintptr(unsafe.Pointer(&elevated)), unsafe.Sizeof(elevated), uintptr(unsafe.Pointer(&returned)))
	if ok == 0 {
		return "", fmt.Errorf("GetTokenInformation: %w", callErr)
	}
	var osVersion struct {
		Size, Major, Minor, Build, Platform uint32
		ServicePack                         [128]uint16
	}
	osVersion.Size = uint32(unsafe.Sizeof(osVersion))
	status, _, _ := syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion").Call(uintptr(unsafe.Pointer(&osVersion)))
	if status != 0 {
		return "", fmt.Errorf("RtlGetVersion failed with status %x", status)
	}
	return fmt.Sprintf("Computer: %s\nAccount: %s\nElevated: %t\nOS: Windows %d.%d, build %d",
		host, account.Username, elevated != 0, osVersion.Major, osVersion.Minor, osVersion.Build), nil
}
