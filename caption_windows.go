package main

import "syscall"

// A maximized window showing the desktop's material through its title bar
// shows the system's window buttons through it too, above MyGo's own: DWM
// draws them in the frame the material extends across the window, where a
// maximized window's frame reaches onto the screen, for a window with a
// window menu (MyGo keeps them out of its panels so, without one). The
// window keeps its menu, and with it the buttons that assistive technology
// finds, while it isn't maximized, where DWM draws none of them.

const wsSysMenu = 0x00080000

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	getWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	setWindowPos     = user32.NewProc("SetWindowPos")
)

// captionStyle is the window's style with its window menu while it isn't
// maximized, and without it while it is.
func captionStyle(style uintptr, maximized bool) uintptr {
	if maximized {
		return style &^ wsSysMenu
	}
	return style | wsSysMenu
}

// setUpCaption keeps the system's buttons from showing over the window's
// while it is maximized.
func (a *app) setUpCaption() {
	apply := func() {
		hwnd := a.win.NativeHandle()
		if hwnd == 0 {
			return
		}
		const gwlStyle = ^uintptr(15) // -16
		style, _, _ := getWindowLongPtr.Call(hwnd, gwlStyle)
		want := captionStyle(style, a.win.IsMaximized())
		if style == 0 || want == style {
			return
		}
		setWindowLongPtr.Call(hwnd, gwlStyle, want)
		// SWP_NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED: the frame is
		// drawn again without them.
		setWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x0001|0x0002|0x0004|0x0010|0x0020)
	}
	a.win.OnMaximize(apply)
	a.win.OnUnmaximize(apply)
	// A window opening maximized, as it last was.
	a.win.OnReadyToShow(apply)
	apply()
}
