package notify

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procNotificationState = shell32.NewProc("SHQueryUserNotificationState")
	clsidQuietHours       = windows.GUID{Data1: 0xf53321fa, Data2: 0x34f8, Data3: 0x4b7f, Data4: [8]byte{0xb9, 0xa3, 0x36, 0x18, 0x77, 0xcb, 0x94, 0xcf}}
	iidQuietHours         = windows.GUID{Data1: 0x6bff4732, Data2: 0x81ec, Data3: 0x4ffb, Data4: [8]byte{0xae, 0x67, 0xb6, 0xc1, 0xbc, 0x29, 0x63, 0x1f}}
)

const (
	clsLocalServer           = 4
	qunsAcceptsNotifications = 5
	qunsApp                  = 7
)

// windowsAllowsSound runs only for an opted-in automatic AgentNet sound.
// The public Shell query reports quiet time and presentation. It cannot
// establish the independent Win11 DND state, so also require known
// unrestricted effective profiles from the read-only shell COM contract.
func (p *soundPlayer) windowsAllowsSound() bool {
	if procNotificationState.Find() != nil {
		return false
	}
	var state uint32
	hr, _, _ := procNotificationState.Call(uintptr(unsafe.Pointer(&state)))
	if hr != 0 || !shellAllowsSound(state) || !p.initCOM() {
		return false
	}
	settings := createCOM(&clsidQuietHours, clsLocalServer, &iidQuietHours)
	if settings == nil {
		return false
	}
	defer settings.release()
	return quietHoursClear(settings)
}

func shellAllowsSound(state uint32) bool {
	return state == qunsAcceptsNotifications || state == qunsApp
}

// Read-only vtable positions from Rafael Rivera's MIT-licensed
// windows_quiethours.idl in Telegram Desktop 0e79d7a4. Query both current
// and selected profiles, plus an active automatic rule, as QueryFocusAssist
// does. Priority-only counts as DND here, regardless of banner exceptions.
func quietHoursClear(settings *comObject) bool {
	const (
		userSelectedProfile      = 3
		quietMomentsManager      = 8
		activeQuietMomentProfile = 10
		activeProfile            = 12
		getActiveQuietMoment     = 6
	)
	if !unrestrictedProfile(settings, activeProfile) || !unrestrictedProfile(settings, userSelectedProfile) {
		return false
	}
	var moments *comObject
	if !settings.call(quietMomentsManager, uintptr(unsafe.Pointer(&moments))) || moments == nil {
		return false
	}
	defer moments.release()
	var active uint32
	return moments.call(getActiveQuietMoment, uintptr(unsafe.Pointer(&active))) &&
		(active == 0 || unrestrictedProfile(settings, activeQuietMomentProfile))
}

func unrestrictedProfile(settings *comObject, slot int) bool {
	var profile *uint16
	ok := settings.call(slot, uintptr(unsafe.Pointer(&profile)))
	if profile == nil {
		return false
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(profile))
	return ok && unrestrictedProfileName(windows.UTF16PtrToString(profile))
}

func unrestrictedProfileName(profile string) bool {
	return strings.HasSuffix(strings.ToLower(profile), ".unrestricted")
}
