import AppKit

// Standalone design preview; does not open a window or access call hardware.
let width: CGFloat = 1360
let height: CGFloat = 990
let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2720, pixelsHigh: 1980,
    bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
    colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
bitmap.size = NSSize(width: width, height: height)
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
NSGraphicsContext.current!.imageInterpolation = .high

func color(_ hex: UInt32) -> NSColor {
    NSColor(srgbRed: CGFloat((hex >> 16) & 255) / 255,
        green: CGFloat((hex >> 8) & 255) / 255,
        blue: CGFloat(hex & 255) / 255, alpha: 1)
}
func rect(_ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat) -> NSRect {
    NSRect(x: x, y: height - y - h, width: w, height: h)
}
func fill(_ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat, _ c: UInt32, _ radius: CGFloat = 0) {
    color(c).setFill()
    NSBezierPath(roundedRect: rect(x, y, w, h), xRadius: radius, yRadius: radius).fill()
}
func text(_ s: String, _ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ size: CGFloat,
          _ c: UInt32, _ weight: NSFont.Weight = .regular, _ alignment: NSTextAlignment = .center) {
    let paragraph = NSMutableParagraphStyle()
    paragraph.alignment = alignment
    (s as NSString).draw(in: rect(x, y, w, size * 1.55), withAttributes: [
        .font: NSFont.systemFont(ofSize: size, weight: weight),
        .foregroundColor: color(c), .paragraphStyle: paragraph
    ])
}
func symbol(_ name: String, _ x: CGFloat, _ y: CGFloat, _ size: CGFloat, _ tint: UInt32) {
    guard let image = NSImage(systemSymbolName: name, accessibilityDescription: nil)?
        .withSymbolConfiguration(NSImage.SymbolConfiguration(pointSize: size, weight: .regular)) else { return }
    let tinted = NSImage(size: NSSize(width: size, height: size))
    tinted.lockFocus()
    image.draw(in: NSRect(x: 0, y: 0, width: size, height: size))
    color(tint).setFill()
    NSRect(x: 0, y: 0, width: size, height: size).fill(using: .sourceIn)
    tinted.unlockFocus()
    tinted.draw(in: rect(x, y, size, size))
}
func control(_ x: CGFloat, _ y: CGFloat, _ icon: String, _ label: String, selected: Bool = false) {
    fill(x, y, 58, 58, selected ? 0xF3F4F3 : 0x383B3A, 29)
    symbol(icon, x + 16, y + 16, 26, selected ? 0x202523 : 0xF2F4F3)
    text(label, x - 12, y + 70, 82, 13, 0xBFC5C1)
}
func panel(_ x: CGFloat, keypad: Bool) {
    let y: CGFloat = 155
    let shadow = NSShadow()
    shadow.shadowColor = NSColor.black.withAlphaComponent(0.14)
    shadow.shadowBlurRadius = 24
    shadow.shadowOffset = NSSize(width: 0, height: -10)
    NSGraphicsContext.saveGraphicsState()
    shadow.set()
    fill(x, y, 390, 718, 0x242826, 16)
    NSGraphicsContext.restoreGraphicsState()
    fill(x + 18, y + 18, 11, 11, 0xEC6860, 6)
    fill(x + 36, y + 18, 11, 11, 0xEDBD4B, 6)
    fill(x + 54, y + 18, 11, 11, 0x60BF56, 6)
    text("DJ 4G Hub", x + 105, y + 15, 180, 12, 0xB4BCB7, .medium)
    symbol("arrow.down.right.and.arrow.up.left", x + 353, y + 17, 15, 0xADB5AF)
    if !keypad {
        fill(x + 151, y + 87, 88, 88, 0x3C4C43, 16)
        symbol("person.fill", x + 173, y + 108, 44, 0xD2E2D7)
        text("133 4870 9339", x + 25, y + 198, 340, 29, 0xFFFFFF, .medium)
        fill(x + 155, y + 250, 6, 6, 0x42D17D, 3)
        text("通话中", x + 166, y + 241, 74, 14, 0xAEBAB1)
        text("02:36", x + 60, y + 280, 270, 20, 0xF0F3F1, .regular)
        control(x + 52, y + 424, "mic.slash", "静音")
        control(x + 166, y + 424, "speaker.wave.2.fill", "扬声器", selected: true)
        control(x + 280, y + 424, "circle.grid.3x3.fill", "键盘")
    } else {
        text("133 4870 9339", x + 25, y + 67, 340, 23, 0xFFFFFF, .medium)
        fill(x + 139, y + 113, 6, 6, 0x42D17D, 3)
        text("通话中  02:36", x + 152, y + 104, 118, 13, 0xAEBAB1)
        text("10086", x + 54, y + 157, 282, 31, 0xFFFFFF, .medium)
        symbol("delete.left", x + 333, y + 165, 21, 0xBBC5BE)
        let digits = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"]
        let letters = ["", "ABC", "DEF", "GHI", "JKL", "MNO", "PQRS", "TUV", "WXYZ", "", "+", ""]
        for i in 0..<12 {
            let dx = x + 58 + CGFloat(i % 3) * 110
            let dy = y + 220 + CGFloat(i / 3) * 76
            fill(dx, dy, 54, 54, 0x383D39, 27)
            text(digits[i], dx, dy + 1, 54, 26, 0xFFFFFF)
            text(letters[i], dx - 6, dy + 35, 66, 8, 0xB8C1BA, .medium)
        }
        symbol("mic.slash", x + 65, y + 549, 22, 0xD4DDD6)
        symbol("speaker.wave.2.fill", x + 184, y + 549, 22, 0x61DD91)
        symbol("chevron.down", x + 304, y + 549, 22, 0xD4DDD6)
        text("静音", x + 38, y + 580, 76, 12, 0xBFC5C1)
        text("扬声器", x + 157, y + 580, 76, 12, 0xBFC5C1)
        text("收起键盘", x + 277, y + 580, 76, 12, 0xBFC5C1)
    }
    let hangY = y + (keypad ? 625 : 590)
    fill(x + 164, hangY, 62, 62, 0xED4D57, 31)
    symbol("phone.down.fill", x + 181, hangY + 17, 28, 0xFFFFFF)
    text("挂断", x + 151, hangY + 68, 88, 12, 0xCCD2CE)
}

fill(0, 0, width, height, 0xF0F2F1)
text("DJ 4G Hub", 100, 35, 1160, 16, 0x48735A, .medium, .left)
text("原生语音通话面板", 100, 66, 1160, 30, 0x202A24, .semibold, .left)
text("通话中", 182, 119, 390, 15, 0x55655B, .medium)
text("展开数字键盘", 788, 119, 390, 15, 0x55655B, .medium)
panel(182, keypad: false)
panel(788, keypad: true)
symbol("arrow.right", 650, 475, 32, 0x94A69A)
text("微信式通话布局 · 独立悬浮窗口 · 接听后保留面板", 100, 918, 1160, 15, 0x64736A)
NSGraphicsContext.restoreGraphicsState()
let output = CommandLine.arguments[1]
try bitmap.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print(output)
