package notify

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procPlaySound   = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")
	ole32           = windows.NewLazySystemDLL("ole32.dll")
	procCoCreate    = ole32.NewProc("CoCreateInstance")
	clsidMMDevice   = windows.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
	iidMMEnumerator = windows.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
	iidSessionMgr   = windows.GUID{Data1: 0xbfa971f1, Data2: 0x4d5e, Data3: 0x40bb, Data4: [8]byte{0x93, 0x5e, 0x96, 0x70, 0x39, 0xbf, 0xbe, 0xe4}}
)

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
	clsInproc    = 1
)

// soundPlayer belongs to the existing notifier window thread. PlaySoundW
// loads no files here and uses this process's audio session without SND_SYSTEM.
// Hold the display name's session control and the WinMM buffer for as long as
// Windows can use them. No extra process, polling, thread, or sound queue.
type soundPlayer struct {
	wave       []byte
	pin        runtime.Pinner
	comReady   bool
	enumerator *comObject
	session    *comObject
	endpoint   string
}

func (p *soundPlayer) play(wave []byte) error {
	if len(wave) == 0 {
		return nil
	}
	if len(p.wave) != 0 && &p.wave[0] != &wave[0] && !p.stop() {
		return errors.New("Windows could not replace the playing notification sound.")
	}
	if len(p.wave) == 0 {
		p.pin.Pin(&wave[0])
		p.wave = wave
	}
	nameOK := p.nameSession()
	if ok, _, _ := procPlaySound.Call(uintptr(unsafe.Pointer(&wave[0])), 0, sndMemory|sndAsync|sndNoDefault); ok == 0 {
		p.stop()
		p.session.release()
		p.session, p.endpoint = nil, ""
		return errors.New("Windows could not play the notification sound. Check your audio output.")
	}
	if !nameOK {
		return errors.New("Windows started playback but could not name the AgentNet audio session.")
	}
	return nil
}

func (p *soundPlayer) stop() bool {
	if len(p.wave) == 0 {
		return true
	}
	if ok, _, _ := procPlaySound.Call(0, 0, 0); ok == 0 {
		return false // retain the buffer if WinMM might still be using it
	}
	p.pin.Unpin()
	p.wave = nil
	return true
}

func (p *soundPlayer) close() {
	p.stop()
	p.session.release()
	p.session, p.endpoint = nil, ""
	p.enumerator.release()
	p.enumerator = nil
	if p.comReady {
		windows.CoUninitialize()
		p.comReady = false
	}
}

func (p *soundPlayer) initCOM() bool {
	if !p.comReady {
		// x/sys represents successful S_FALSE (already initialized) as Errno(1).
		err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		p.comReady = err == nil || err == syscall.Errno(1)
	}
	return p.comReady
}

func (p *soundPlayer) nameSession() bool {
	if !p.initCOM() {
		return false
	}
	if p.enumerator == nil {
		p.enumerator = createCOM(&clsidMMDevice, clsInproc, &iidMMEnumerator)
		if p.enumerator == nil {
			return false
		}
	}
	// IMMDeviceEnumerator::GetDefaultAudioEndpoint(eRender, eConsole).
	var device *comObject
	if !p.enumerator.call(4, 0, 0, uintptr(unsafe.Pointer(&device))) || device == nil {
		return false
	}
	defer device.release()
	// IMMDevice::GetId lets a change of Windows output refresh the label.
	var deviceID *uint16
	if !device.call(5, uintptr(unsafe.Pointer(&deviceID))) || deviceID == nil {
		return false
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(deviceID))
	id := windows.UTF16PtrToString(deviceID)
	if p.session != nil && p.endpoint == id {
		return true
	}
	var manager *comObject
	if !device.call(3, uintptr(unsafe.Pointer(&iidSessionMgr)), clsInproc, 0, uintptr(unsafe.Pointer(&manager))) || manager == nil {
		return false
	}
	defer manager.release()
	// IAudioSessionManager::GetAudioSessionControl(GUID_NULL, 0).
	var control *comObject
	if !manager.call(3, 0, 0, uintptr(unsafe.Pointer(&control))) || control == nil {
		return false
	}
	name, _ := windows.UTF16PtrFromString("AgentNet")
	// IAudioSessionControl::SetDisplayName.
	if !control.call(5, uintptr(unsafe.Pointer(name)), 0) {
		control.release()
		return false
	}
	p.session.release()
	p.session, p.endpoint = control, id
	return true
}

type comObject struct{ vtable *[32]uintptr }

func createCOM(class *windows.GUID, context uint32, iid *windows.GUID) *comObject {
	var object *comObject
	hr, _, _ := procCoCreate.Call(uintptr(unsafe.Pointer(class)), 0, uintptr(context), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&object)))
	if int32(hr) < 0 {
		return nil
	}
	return object
}

//go:uintptrescapes
func (o *comObject) call(slot int, args ...uintptr) bool {
	var parameters [6]uintptr
	parameters[0] = uintptr(unsafe.Pointer(o))
	copy(parameters[1:], args)
	hr, _, _ := syscall.SyscallN(o.vtable[slot], parameters[:len(args)+1]...)
	runtime.KeepAlive(o)
	return int32(hr) >= 0
}

func (o *comObject) release() {
	if o != nil {
		o.call(2) // IUnknown::Release
	}
}
