#import "../call_window_darwin.m"

#define CHECK(condition, message) if (!(condition)) { fprintf(stderr, "%s\n", message); return 1; }

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        DJCallWindow *panel = [[DJCallWindow alloc] init];
        [panel show];
        CHECK(!panel.number.hidden && !panel.digits[0].hidden, "idle dialpad is missing")
        CHECK(!panel.hangup.enabled, "empty number is callable")
        panel.number.stringValue = @"+86 133-1234-5678;ATH";
        [panel controlTextDidChange:nil];
        CHECK([panel.draft isEqual:@"+8613312345678"], "pasted number was not normalized")
        CHECK(panel.hangup.enabled, "valid number cannot be dialed")
        [panel hangupCall:nil];
        CHECK(panel.pending, "dial allows duplicate submissions")
        NSDictionary *ring = @{@"id":@"call-1", @"number":@"13312345678",
            @"local_audio":@YES, @"direction":@"inbound", @"state":@"ringing"};
        [panel update:@{@"calls":@[ring], @"sequence":@(panel.pendingSequence)}];
        CHECK(!panel.answer.hidden && panel.digits[0].hidden, "incoming controls are incorrect")
        [panel answerCall:nil];
        NSInteger answerSequence = panel.pendingSequence;
        NSMutableDictionary *active = [ring mutableCopy];
        active[@"state"] = @"active";
        active[@"connected_at"] = @"2026-11-07T20:00:00.123+08:00";
        NSDictionary *audio = @{@"state":@"running", @"microphone_muted":@NO, @"speaker_muted":@NO};
        [panel update:@{@"calls":@[active], @"audio":audio, @"sequence":@(answerSequence)}];
        CHECK(panel.answer.hidden && !panel.mute.hidden && !panel.duration.hidden, "answer did not preserve call panel")
        CHECK(!panel.pending && panel.connectedAt != nil, "answer acknowledgement failed")
        [panel toggleKeypad:nil];
        CHECK(!panel.digits[11].hidden && panel.digits[11].enabled, "connected keypad is unavailable")
        [panel appendDigit:@"#"];
        CHECK([panel.tones isEqual:@"#"], "DTMF digit is not displayed")
        [panel toggleMute:nil];
        CHECK(panel.pending, "mute action was not sent")
        [panel update:@{@"calls":@[active], @"audio":@{@"state":@"running", @"microphone_muted":@YES},
            @"sequence":@(panel.pendingSequence)}];
        CHECK(!panel.pending && [panel.audio[@"microphone_muted"] boolValue], "mute acknowledgement failed")
        [panel toggleSpeaker:nil];
        CHECK(panel.pending, "speaker action was not sent")
        [panel update:@{@"calls":@[active], @"audio":@{@"state":@"running", @"microphone_muted":@YES, @"speaker_muted":@YES},
            @"sequence":@(panel.pendingSequence)}];
        CHECK(!panel.pending && [panel.audio[@"speaker_muted"] boolValue], "speaker acknowledgement failed")
        [panel hangupCall:nil];
        CHECK(panel.pending, "hangup action was not sent")
        NSMutableDictionary *ended = [active mutableCopy];
        ended[@"state"] = @"ended";
        [panel update:@{@"calls":@[ended], @"audio":@{@"state":@"idle"}, @"sequence":@(panel.pendingSequence)}];
        CHECK(![panel live] && !panel.number.hidden && panel.mute.hidden, "hangup did not restore dialpad")
        NSMutableDictionary *next = [ring mutableCopy];
        next[@"id"] = @"call-2";
        next[@"number"] = @"unknown";
        [panel update:@{@"calls":@[ended, next]}];
        CHECK([panel.tones isEqual:@""] && !panel.expanded, "next incoming call inherited old keypad")
        CHECK([panel.caller.stringValue isEqual:@"正在获取号码…"], "caller ID loading state missing")
        next[@"number"] = @"12345";
        [panel update:@{@"calls":@[ended, next]}];
        CHECK([panel.caller.stringValue isEqual:@"12345"], "late caller ID was not updated")
        next[@"local_audio"] = @NO;
        next[@"state"] = @"active";
        [panel update:@{@"calls":@[ended, next]}];
        CHECK(![panel live], "SIP client takeover left local call controls active")
        [panel.window orderOut:nil];
        fprintf(stderr, "native panel transitions passed\n");
        return 0;
    }
}
