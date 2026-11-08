//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework AppKit -framework QuartzCore
void dj_call_window(void);
*/
import "C"

import (
	"os"
	"runtime"
)

const nativeCallWindowAvailable = true

func init() {
	if len(os.Args) == 2 && (os.Args[1] == "--call-window" || os.Args[1] == "--incoming-call-window") {
		runtime.LockOSThread()
		C.dj_call_window()
		os.Exit(0)
	}
}
