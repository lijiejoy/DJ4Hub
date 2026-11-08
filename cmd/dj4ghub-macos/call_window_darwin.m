//go:build darwin && cgo

#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>
#include <stdio.h>

static NSColor *DJColor(unsigned value) {
    return [NSColor colorWithSRGBRed:((value >> 16) & 255) / 255.0
        green:((value >> 8) & 255) / 255.0 blue:(value & 255) / 255.0 alpha:1];
}

@interface DJCallContent : NSView
@end
@implementation DJCallContent
- (BOOL)isFlipped { return YES; }
- (BOOL)acceptsFirstResponder { return YES; }
@end

@interface DJCallButton : NSButton
@end
@implementation DJCallButton
- (void)highlight:(BOOL)flag {
    [super highlight:flag];
    self.alphaValue = flag ? 0.65 : (self.enabled ? 1 : 0.35);
}
- (void)setEnabled:(BOOL)enabled {
    [super setEnabled:enabled];
    self.alphaValue = enabled ? 1 : 0.35;
}
@end

@interface DJCallWindow : NSObject <NSWindowDelegate, NSTextFieldDelegate>
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) NSTextField *caller;
@property(nonatomic, strong) NSTextField *status;
@property(nonatomic, strong) NSTextField *duration;
@property(nonatomic, strong) NSTextField *message;
@property(nonatomic, strong) NSTextField *number;
@property(nonatomic, strong) NSImageView *avatar;
@property(nonatomic, strong) NSButton *answer;
@property(nonatomic, strong) NSButton *hangup;
@property(nonatomic, strong) NSButton *mute;
@property(nonatomic, strong) NSButton *speaker;
@property(nonatomic, strong) NSButton *keypad;
@property(nonatomic, strong) NSButton *erase;
@property(nonatomic, strong) NSTextField *answerLabel;
@property(nonatomic, strong) NSTextField *hangupLabel;
@property(nonatomic, strong) NSTextField *muteLabel;
@property(nonatomic, strong) NSTextField *speakerLabel;
@property(nonatomic, strong) NSTextField *keypadLabel;
@property(nonatomic, strong) NSArray<NSButton *> *digits;
@property(nonatomic, strong) NSDictionary *call;
@property(nonatomic, strong) NSDictionary *audio;
@property(nonatomic, strong) NSDate *connectedAt;
@property(nonatomic, strong) NSDate *ringingAt;
@property(nonatomic, copy) NSString *draft;
@property(nonatomic, copy) NSString *tones;
@property(nonatomic, copy) NSString *callID;
@property(nonatomic) BOOL expanded;
@property(nonatomic) BOOL pending;
@property(nonatomic) NSInteger sequence;
@property(nonatomic) NSInteger pendingSequence;
@end

@implementation DJCallWindow
- (NSTextField *)label:(NSString *)value size:(CGFloat)size color:(NSColor *)color frame:(NSRect)frame {
    NSTextField *label = [NSTextField labelWithString:value];
    label.frame = frame;
    label.font = [NSFont systemFontOfSize:size weight:NSFontWeightMedium];
    label.textColor = color;
    label.alignment = NSTextAlignmentCenter;
    [self.window.contentView addSubview:label];
    return label;
}
- (void)icon:(NSString *)name on:(NSButton *)button selected:(BOOL)selected {
    NSImage *image = [NSImage imageWithSystemSymbolName:name accessibilityDescription:button.toolTip];
    button.image = [image imageWithSymbolConfiguration:
        [NSImageSymbolConfiguration configurationWithPointSize:25 weight:NSFontWeightRegular]];
    button.contentTintColor = selected ? DJColor(0x242826) : NSColor.whiteColor;
    button.layer.backgroundColor = (selected ? DJColor(0xf3f4f3) : DJColor(0x383b3a)).CGColor;
}
- (NSButton *)button:(NSString *)icon label:(NSString *)label action:(SEL)action frame:(NSRect)frame {
    DJCallButton *button = [[DJCallButton alloc] initWithFrame:frame];
    button.bordered = NO;
    button.wantsLayer = YES;
    button.layer.cornerRadius = frame.size.width / 2;
    button.imagePosition = NSImageOnly;
    button.target = self;
    button.action = action;
    button.toolTip = label;
    [button setAccessibilityLabel:label];
    [self icon:icon on:button selected:NO];
    [self.window.contentView addSubview:button];
    return button;
}
- (void)show {
    self.draft = @"";
    self.tones = @"";
    self.callID = @"";
    self.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 390, 718)
        styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
            NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskFullSizeContentView
        backing:NSBackingStoreBuffered defer:NO];
    self.window.title = @"DJ 4G Hub · 电话";
    self.window.titleVisibility = NSWindowTitleHidden;
    self.window.titlebarAppearsTransparent = YES;
    self.window.backgroundColor = DJColor(0x242826);
    self.window.movableByWindowBackground = YES;
    self.window.level = NSFloatingWindowLevel;
    self.window.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary;
    self.window.delegate = self;
    self.window.releasedWhenClosed = NO;
    self.window.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
    self.window.contentView = [[DJCallContent alloc] initWithFrame:NSMakeRect(0, 0, 390, 718)];
    [self label:@"DJ 4G Hub" size:12 color:DJColor(0xb4bcb7) frame:NSMakeRect(105, 13, 180, 22)];
    self.avatar = [[NSImageView alloc] initWithFrame:NSMakeRect(151, 87, 88, 88)];
    self.avatar.wantsLayer = YES;
    self.avatar.layer.cornerRadius = 8;
    self.avatar.layer.backgroundColor = DJColor(0x3c4c43).CGColor;
    self.avatar.image = [[NSImage imageWithSystemSymbolName:@"person.fill" accessibilityDescription:@"联系人"]
        imageWithSymbolConfiguration:[NSImageSymbolConfiguration configurationWithPointSize:40 weight:NSFontWeightRegular]];
    self.avatar.contentTintColor = DJColor(0xd2e2d7);
    self.avatar.imageScaling = NSImageScaleNone;
    [self.window.contentView addSubview:self.avatar];
    self.caller = [self label:@"" size:28 color:NSColor.whiteColor frame:NSMakeRect(20, 198, 350, 42)];
    self.caller.cell.lineBreakMode = NSLineBreakByTruncatingMiddle;
    self.status = [self label:@"" size:14 color:DJColor(0xb8c4bc) frame:NSMakeRect(20, 246, 350, 24)];
    self.duration = [self label:@"" size:20 color:DJColor(0xf0f3f1) frame:NSMakeRect(60, 280, 270, 30)];
    self.message = [self label:@"" size:12 color:DJColor(0xffb3b6) frame:NSMakeRect(25, 346, 340, 60)];
    self.message.maximumNumberOfLines = 3;
    self.message.cell.wraps = YES;
    self.number = [[NSTextField alloc] initWithFrame:NSMakeRect(45, 151, 290, 50)];
    self.number.bordered = NO;
    self.number.drawsBackground = NO;
    self.number.focusRingType = NSFocusRingTypeNone;
    self.number.font = [NSFont monospacedDigitSystemFontOfSize:29 weight:NSFontWeightMedium];
    self.number.textColor = NSColor.whiteColor;
    self.number.alignment = NSTextAlignmentCenter;
    self.number.placeholderString = @"电话号码";
    self.number.delegate = self;
    [self.number setAccessibilityLabel:@"电话号码"];
    [self.window.contentView addSubview:self.number];
    self.erase = [self button:@"delete.left" label:@"删除最后一位" action:@selector(eraseDigit:) frame:NSMakeRect(337, 159, 34, 34)];
    self.erase.layer.backgroundColor = NSColor.clearColor.CGColor;
    NSMutableArray *digits = [NSMutableArray array];
    NSArray *values = @[@"1", @"2", @"3", @"4", @"5", @"6", @"7", @"8", @"9", @"*", @"0", @"#"];
    NSArray *letters = @[@"", @"ABC", @"DEF", @"GHI", @"JKL", @"MNO", @"PQRS", @"TUV", @"WXYZ", @"", @"+", @""];
    for (NSInteger i = 0; i < 12; i++) {
        NSButton *button = [self button:@"circle" label:values[i] action:@selector(pressDigit:)
            frame:NSMakeRect(56 + (i % 3) * 110, 220 + (i / 3) * 76, 58, 58)];
        button.tag = i;
        button.image = nil;
        button.imagePosition = NSNoImage;
        NSMutableParagraphStyle *paragraph = [[NSMutableParagraphStyle alloc] init];
        paragraph.alignment = NSTextAlignmentCenter;
        NSMutableAttributedString *title = [[NSMutableAttributedString alloc] initWithString:values[i]
            attributes:@{NSFontAttributeName:[NSFont systemFontOfSize:26],
                NSForegroundColorAttributeName:NSColor.whiteColor, NSParagraphStyleAttributeName:paragraph}];
        if ([letters[i] length]) {
            [title appendAttributedString:[[NSAttributedString alloc] initWithString:
                [@"\n" stringByAppendingString:letters[i]] attributes:@{
                    NSFontAttributeName:[NSFont systemFontOfSize:8 weight:NSFontWeightMedium],
                    NSForegroundColorAttributeName:DJColor(0xb8c1ba), NSParagraphStyleAttributeName:paragraph}]];
        }
        button.attributedTitle = title;
        [digits addObject:button];
    }
    self.digits = digits;
    self.mute = [self button:@"mic.slash" label:@"静音" action:@selector(toggleMute:) frame:NSMakeRect(52, 424, 58, 58)];
    self.speaker = [self button:@"speaker.wave.2.fill" label:@"扬声器" action:@selector(toggleSpeaker:) frame:NSMakeRect(166, 424, 58, 58)];
    self.keypad = [self button:@"circle.grid.3x3.fill" label:@"键盘" action:@selector(toggleKeypad:) frame:NSMakeRect(280, 424, 58, 58)];
    self.muteLabel = [self label:@"静音" size:13 color:DJColor(0xbfc5c1) frame:NSMakeRect(40, 494, 82, 22)];
    self.speakerLabel = [self label:@"扬声器" size:13 color:DJColor(0xbfc5c1) frame:NSMakeRect(154, 494, 82, 22)];
    self.keypadLabel = [self label:@"键盘" size:13 color:DJColor(0xbfc5c1) frame:NSMakeRect(268, 494, 82, 22)];
    self.hangup = [self button:@"phone.down.fill" label:@"挂断" action:@selector(hangupCall:) frame:NSMakeRect(164, 590, 62, 62)];
    self.answer = [self button:@"phone.fill" label:@"接听" action:@selector(answerCall:) frame:NSMakeRect(252, 590, 62, 62)];
    self.answer.layer.backgroundColor = DJColor(0x1ab962).CGColor;
    self.hangupLabel = [self label:@"挂断" size:12 color:DJColor(0xccd2ce) frame:NSMakeRect(151, 658, 88, 22)];
    self.answerLabel = [self label:@"接听" size:12 color:DJColor(0xccd2ce) frame:NSMakeRect(239, 658, 88, 22)];
    [self render];
    [self.window center];
    [self focus];
    [NSTimer scheduledTimerWithTimeInterval:0.5 target:self selector:@selector(tick:) userInfo:nil repeats:YES];
    [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown handler:^NSEvent *(NSEvent *event) {
        if (event.window != self.window || (event.modifierFlags & NSEventModifierFlagCommand)) return event;
        if (self.number.editable && [self.window.firstResponder isKindOfClass:NSTextView.class]) return event;
        NSString *key = event.characters;
        if (key.length == 1 && [@"0123456789*#" containsString:key]) {
            [self appendDigit:key]; return nil;
        }
        if (event.keyCode == 51) { [self eraseDigit:nil]; return nil; }
        if (event.keyCode == 53 && self.expanded) { [self toggleKeypad:nil]; return nil; }
        return event;
    }];
}
- (void)focus {
    [self.window deminiaturize:nil];
    [self.window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
    if (![self live]) [self.window makeFirstResponder:self.number];
}
- (BOOL)live {
    return [@[@"dialing", @"ringing", @"active"] containsObject:self.call[@"state"] ?: @""];
}
- (BOOL)incoming {
    return [self.call[@"direction"] isEqual:@"inbound"] && [self.call[@"state"] isEqual:@"ringing"];
}
- (void)render {
    BOOL live = [self live], incoming = [self incoming];
    BOOL active = [self.call[@"state"] isEqual:@"active"];
    BOOL keypad = !live || (active && self.expanded);
    self.avatar.hidden = keypad;
    self.caller.frame = keypad ? NSMakeRect(20, 67, 350, 36) : NSMakeRect(20, 198, 350, 42);
    self.caller.font = [NSFont systemFontOfSize:keypad ? 23 : 28 weight:NSFontWeightMedium];
    NSString *number = self.call[@"number"];
    BOOL known = number.length && ![number isEqual:@"unknown"];
    self.caller.stringValue = live ? (known ? number :
        (self.ringingAt && -self.ringingAt.timeIntervalSinceNow > 5 ? @"未提供号码" : @"正在获取号码…")) : @"拨打电话";
    self.status.frame = keypad ? NSMakeRect(20, 108, 350, 24) : NSMakeRect(20, 246, 350, 24);
    NSString *state = self.call[@"state"];
    if (!live) {
        self.status.stringValue = self.call ? @"通话已结束" : @"";
    } else if (incoming) {
        self.status.stringValue = @"邀请你进行语音通话";
    } else if (active) {
        self.status.stringValue = [self.audio[@"state"] isEqual:@"failed"] ? @"声音连接失败" :
            ([self.audio[@"state"] isEqual:@"running"] ? @"通话中" : @"正在连接声音…");
    } else {
        self.status.stringValue = [state isEqual:@"dialing"] ? @"正在拨号…" : @"等待对方接听…";
    }
    self.duration.hidden = !active;
    self.duration.frame = keypad ? NSMakeRect(264, 110, 90, 20) : NSMakeRect(60, 280, 270, 30);
    self.duration.font = [NSFont monospacedDigitSystemFontOfSize:keypad ? 13 : 20 weight:NSFontWeightRegular];
    [self tick:nil];
    self.number.hidden = !keypad;
    self.number.editable = !live;
    self.number.selectable = !live;
    if (![self.window.firstResponder isKindOfClass:NSTextView.class]) {
        self.number.stringValue = live ? self.tones : self.draft;
    }
    self.erase.hidden = !keypad;
    self.erase.enabled = !self.pending;
    for (NSButton *digit in self.digits) {
        digit.hidden = !keypad;
        digit.enabled = !self.pending && (active || !live) &&
            (live || (digit.tag != 9 && digit.tag != 11));
    }
    BOOL controls = live && !incoming;
    self.mute.hidden = self.speaker.hidden = self.keypad.hidden = !controls;
    self.muteLabel.hidden = self.speakerLabel.hidden = self.keypadLabel.hidden = !controls;
    CGFloat controlY = keypad ? 535 : 424;
    self.mute.frame = NSMakeRect(52, controlY, 58, 58);
    self.speaker.frame = NSMakeRect(166, controlY, 58, 58);
    self.keypad.frame = NSMakeRect(280, controlY, 58, 58);
    self.muteLabel.frame = NSMakeRect(40, controlY + 62, 82, 22);
    self.speakerLabel.frame = NSMakeRect(154, controlY + 62, 82, 22);
    self.keypadLabel.frame = NSMakeRect(268, controlY + 62, 82, 22);
    self.mute.enabled = self.speaker.enabled = active && !self.pending;
    self.keypad.enabled = active;
    [self icon:@"mic.slash" on:self.mute selected:[self.audio[@"microphone_muted"] boolValue]];
    [self icon:@"speaker.wave.2.fill" on:self.speaker selected:![self.audio[@"speaker_muted"] boolValue]];
    self.mute.toolTip = [self.audio[@"microphone_muted"] boolValue] ? @"取消麦克风静音" : @"将麦克风静音";
    self.speaker.toolTip = [self.audio[@"speaker_muted"] boolValue] ? @"打开扬声器" : @"关闭扬声器";
    [self icon:keypad ? @"chevron.down" : @"circle.grid.3x3.fill" on:self.keypad selected:NO];
    self.keypadLabel.stringValue = keypad ? @"收起键盘" : @"键盘";
    self.keypad.toolTip = self.keypadLabel.stringValue;
    CGFloat hangY = keypad ? 625 : 590;
    self.answer.hidden = self.answerLabel.hidden = !incoming;
    self.hangup.frame = NSMakeRect(incoming ? 76 : 164, hangY, 62, 62);
    self.hangupLabel.frame = NSMakeRect(incoming ? 63 : 151, hangY + 66, 88, 22);
    self.hangup.enabled = self.answer.enabled = !self.pending;
    [self icon:live ? @"phone.down.fill" : @"phone.fill" on:self.hangup selected:NO];
    self.hangup.layer.backgroundColor = (live ? DJColor(0xed4d57) : DJColor(0x1ab962)).CGColor;
    self.hangupLabel.stringValue = incoming ? @"拒接" : (live ? @"挂断" : @"拨打");
    self.hangup.toolTip = self.hangupLabel.stringValue;
    [self.hangup setAccessibilityLabel:self.hangupLabel.stringValue];
    if (!live) self.hangup.enabled = !self.pending && self.draft.length >= 3;
    self.message.frame = keypad ? NSMakeRect(25, 197, 340, 22) : NSMakeRect(25, 346, 340, 60);
    self.message.maximumNumberOfLines = keypad ? 1 : 3;
}
- (void)tick:(NSTimer *)timer {
    if (!self.connectedAt) { self.duration.stringValue = @""; return; }
    NSInteger seconds = MAX(0, (NSInteger)-self.connectedAt.timeIntervalSinceNow);
    self.duration.stringValue = [NSString stringWithFormat:@"%02ld:%02ld", seconds / 60, seconds % 60];
}
- (void)update:(NSDictionary *)data {
    if ([data[@"focus"] boolValue]) [self focus];
    NSArray *calls = [data[@"calls"] isKindOfClass:NSArray.class] ? data[@"calls"] : @[];
    NSDictionary *current = nil, *live = nil;
    for (NSDictionary *call in calls) {
        if ([call[@"id"] isEqual:self.callID] && [call[@"local_audio"] boolValue]) current = call;
        if ([call[@"local_audio"] boolValue] && [@[@"dialing", @"ringing", @"active"] containsObject:call[@"state"]]) live = call;
    }
    NSDictionary *next = live ?: current;
    if (next && ![next[@"id"] isEqual:self.callID]) {
        self.callID = next[@"id"];
        self.tones = @"";
        self.expanded = NO;
        self.connectedAt = nil;
        self.ringingAt = [NSDate date];
        self.message.stringValue = @"";
        [self.window makeFirstResponder:self.window.contentView];
    }
    self.call = next;
    if ([next[@"state"] isEqual:@"active"] && !self.connectedAt) {
        NSISO8601DateFormatter *formatter = [[NSISO8601DateFormatter alloc] init];
        formatter.formatOptions = NSISO8601DateFormatWithInternetDateTime | NSISO8601DateFormatWithFractionalSeconds;
        self.connectedAt = [formatter dateFromString:next[@"connected_at"] ?: @""] ?: [NSDate date];
    }
    self.audio = [data[@"audio"] isKindOfClass:NSDictionary.class] ? data[@"audio"] : @{};
    if ([data[@"sequence"] integerValue] == self.pendingSequence && self.pendingSequence > 0) {
        self.pending = NO;
        self.pendingSequence = 0;
    }
    if ([data[@"error"] isKindOfClass:NSString.class] && [data[@"error"] length]) {
        self.message.stringValue = data[@"error"];
        self.message.toolTip = data[@"error"];
    }
    if ([self.audio[@"state"] isEqual:@"failed"] && [self live]) {
        self.message.stringValue = @"音频连接失败，请检查麦克风权限与设备";
        self.message.toolTip = self.audio[@"error"];
    }
    [self render];
}
- (void)send:(NSMutableDictionary *)action blocking:(BOOL)blocking {
    action[@"sequence"] = @(++self.sequence);
    if (self.callID.length) action[@"call_id"] = self.callID;
    if (blocking) {
        self.pending = YES;
        self.pendingSequence = self.sequence;
    }
    self.message.stringValue = @"";
    NSData *data = [NSJSONSerialization dataWithJSONObject:action options:0 error:nil];
    fwrite(data.bytes, 1, data.length, stdout);
    fputc('\n', stdout);
    fflush(stdout);
    [self render];
}
- (void)controlTextDidChange:(NSNotification *)notification {
    NSMutableString *filtered = [NSMutableString string];
    NSString *value = self.number.stringValue;
    for (NSUInteger i = 0; i < value.length && filtered.length < 16; i++) {
        unichar c = [value characterAtIndex:i];
        if ((c >= '0' && c <= '9') || (c == '+' && filtered.length == 0)) [filtered appendFormat:@"%C", c];
    }
    if (![value isEqual:filtered]) self.number.stringValue = filtered;
    self.draft = filtered;
    self.hangup.enabled = !self.pending && filtered.length >= 3;
}
- (BOOL)control:(NSControl *)control textView:(NSTextView *)textView doCommandBySelector:(SEL)selector {
    if (selector == @selector(insertNewline:)) { [self hangupCall:nil]; return YES; }
    return NO;
}
- (void)appendDigit:(NSString *)value {
    if (self.pending) return;
    if ([self live]) {
        if (![self.call[@"state"] isEqual:@"active"]) return;
        self.tones = [self.tones stringByAppendingString:value];
        if (self.tones.length > 16) self.tones = [self.tones substringFromIndex:self.tones.length - 16];
        [self send:[@{@"action":@"dtmf", @"digit":value} mutableCopy] blocking:NO];
    } else if (self.draft.length < 16 && ![value isEqual:@"*"] && ![value isEqual:@"#"]) {
        self.draft = [self.draft stringByAppendingString:value];
    }
    self.number.stringValue = [self live] ? self.tones : self.draft;
    [self render];
}
- (void)pressDigit:(NSButton *)sender {
    NSArray *values = @[@"1", @"2", @"3", @"4", @"5", @"6", @"7", @"8", @"9", @"*", @"0", @"#"];
    [self appendDigit:values[sender.tag]];
}
- (void)eraseDigit:(id)sender {
    if (self.pending) return;
    NSString *value = [self live] ? self.tones : self.draft;
    if (value.length) value = [value substringToIndex:value.length - 1];
    if ([self live]) self.tones = value; else self.draft = value;
    self.number.stringValue = value;
    [self render];
}
- (void)answerCall:(id)sender {
    if (!self.pending && [self incoming]) [self send:[@{@"action":@"answer"} mutableCopy] blocking:YES];
}
- (void)hangupCall:(id)sender {
    if (self.pending) return;
    if ([self live]) {
        [self send:[@{@"action":@"hangup"} mutableCopy] blocking:YES];
    } else if (self.draft.length >= 3) {
        [self send:[@{@"action":@"dial", @"number":self.draft} mutableCopy] blocking:YES];
    }
}
- (void)toggleMute:(id)sender {
    [self send:[@{@"action":@"audio",
        @"microphone_muted":[NSNumber numberWithBool:![self.audio[@"microphone_muted"] boolValue]],
        @"speaker_muted":[NSNumber numberWithBool:[self.audio[@"speaker_muted"] boolValue]]} mutableCopy] blocking:YES];
}
- (void)toggleSpeaker:(id)sender {
    [self send:[@{@"action":@"audio",
        @"microphone_muted":[NSNumber numberWithBool:[self.audio[@"microphone_muted"] boolValue]],
        @"speaker_muted":[NSNumber numberWithBool:![self.audio[@"speaker_muted"] boolValue]]} mutableCopy] blocking:YES];
}
- (void)toggleKeypad:(id)sender {
    self.expanded = !self.expanded;
    [self.window makeFirstResponder:self.window.contentView];
    [self render];
}
- (BOOL)windowShouldClose:(NSWindow *)sender {
    [NSApp terminate:nil];
    return YES;
}
@end

void dj_call_window(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        DJCallWindow *controller = [[DJCallWindow alloc] init];
        [controller show];
        dispatch_async(dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{
            char *line = NULL;
            size_t capacity = 0;
            while (getline(&line, &capacity, stdin) != -1) {
                NSData *data = [[NSString stringWithUTF8String:line] dataUsingEncoding:NSUTF8StringEncoding];
                NSDictionary *event = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
                if ([event isKindOfClass:NSDictionary.class]) {
                    dispatch_async(dispatch_get_main_queue(), ^{ [controller update:event]; });
                }
            }
            free(line);
            dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
        });
        [NSApp run];
    }
}
