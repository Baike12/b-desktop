//go:build darwin

package app

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

static void bdesktop_set_dock_icon(const void *png, int length) {
	NSData *data = [NSData dataWithBytes:png length:(NSUInteger)length];
	NSImage *image = [[NSImage alloc] initWithData:data];
	if (image == nil) {
		return;
	}
	// AppKit 图标切换必须发生在主线程。
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp setApplicationIconImage:image];
	});
}
*/
import "C"

import (
	"runtime"
	"unsafe"
)

func setDockIcon(png []byte) {
	if len(png) == 0 {
		return
	}
	C.bdesktop_set_dock_icon(unsafe.Pointer(&png[0]), C.int(len(png)))
	runtime.KeepAlive(png)
}
