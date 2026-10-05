package main

import (
	"syscall"
	"unsafe"
)

// systemPowerStatus is SYSTEM_POWER_STATUS (winbase.h).
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var getSystemPowerStatus = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// hasBattery reports a system battery: BatteryFlag is neither 128 (no
// system battery) nor 255 (unknown).
func hasBattery() bool {
	var s systemPowerStatus
	if r, _, _ := getSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); r == 0 {
		return false
	}
	return s.BatteryFlag != 128 && s.BatteryFlag != 255
}
