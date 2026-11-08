//go:build darwin && cgo

#import <AVFoundation/AVFoundation.h>
#import <Foundation/Foundation.h>
#include "native_audio.h"
#include <stdio.h>

int dj_audio_authorization(void) {
    @autoreleasepool {
        return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    }
}

int dj_audio_permission(char *error, int error_size) {
    @autoreleasepool {
        AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        if (status == AVAuthorizationStatusAuthorized) return 0;
        if (status == AVAuthorizationStatusNotDetermined) {
            dispatch_semaphore_t signal = dispatch_semaphore_create(0);
            dispatch_retain(signal);
            [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
                dispatch_semaphore_signal(signal);
                dispatch_release(signal);
            }];
            long timed_out = dispatch_semaphore_wait(signal, dispatch_time(DISPATCH_TIME_NOW, 12 * NSEC_PER_SEC));
            dispatch_release(signal);
            if (!timed_out && [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio] == AVAuthorizationStatusAuthorized) return 0;
        }
        snprintf(error, error_size, "Microphone permission is required for module USB audio. Allow DJ 4G Hub or its launching app in System Settings > Privacy & Security > Microphone, then retry.");
        return -1;
    }
}
