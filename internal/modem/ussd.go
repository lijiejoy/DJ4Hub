package modem

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/WongLoki/DJ4Hub/pkg/logger"
	"github.com/WongLoki/DJ4Hub/pkg/smscodec"
	"github.com/warthog618/sms/encoding/gsm7"
)

// USSD helpers (Item 2): command building, +CUSD line parsing and text
// decoding, shared by the serial-AT Manager path and the USB-AT bridge
// path in cmd/dj4ghub-macos (which has no Manager).

// BuildCUSDCommand builds AT+CUSD=1,"<input>",15. The input is validated:
// USSD strings are at most 182 octets, and a double quote or control
// character would break out of the AT string and must be rejected, never
// escaped into the command.
func BuildCUSDCommand(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("USSD 内容为空")
	}
	if len(input) > 182 {
		return "", fmt.Errorf("USSD 内容过长（%d 字符，上限 182）", len(input))
	}
	for _, r := range input {
		if r == '"' || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("USSD 内容包含非法字符 %q", r)
		}
	}
	return fmt.Sprintf(`AT+CUSD=1,"%s",15`, input), nil
}

// ParseCUSDLine parses a "+CUSD: <n>,\"<text>\"[,<dcs>]" URC line. The text
// is quoted and may itself contain commas, so it is parsed with a small
// state machine instead of a naive comma split. It returns ok=false for
// anything that is not a +CUSD line.
func ParseCUSDLine(line string) (status, dcs int, text string, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "+CUSD:") {
		return 0, 0, "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(s, "+CUSD:"))
	// <n>
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return 0, 0, "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:comma]))
	if err != nil {
		return 0, 0, "", false
	}
	rest = strings.TrimSpace(rest[comma+1:])
	dcs = -1
	if strings.HasPrefix(rest, "\"") {
		// Quoted text: read to the closing quote.
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return 0, 0, "", false
		}
		text = rest[1 : 1+end]
		rest = strings.TrimSpace(rest[1+end+1:])
		if strings.HasPrefix(rest, ",") {
			dcs, _ = strconv.Atoi(strings.TrimSpace(rest[1:]))
		}
	} else {
		// Unquoted text: up to the next comma or end of line.
		if idx := strings.IndexByte(rest, ','); idx >= 0 {
			text = strings.TrimSpace(rest[:idx])
			dcs, _ = strconv.Atoi(strings.TrimSpace(rest[idx+1:]))
		} else {
			text = strings.TrimSpace(rest)
		}
	}
	return n, dcs, text, true
}

// DecodeUSSDText decodes USSD text per DCS (3GPP TS 23.038). It is the
// package-level form of Manager.decodeUSSDText so the USB-AT path can
// reuse it without a Manager.
func DecodeUSSDText(raw string, dcs int) string {
	if raw == "" {
		return ""
	}

	// 判断是否为 Hex 字符串（偶数长度、全 hex 字符）
	isHex := smscodec.IsHexString(raw)

	// DCS 高 4 位判断编码类型
	// 0x00-0x03 (0-3): GSM 7-bit
	// 0x04-0x07 (4-7): 8-bit data
	// 0x08-0x0B (8-11): UCS2
	// 0x0F (15): GSM 7-bit (unspecified)
	// 0x48 (72): UCS2
	codingGroup := (dcs >> 4) & 0x0F
	alphabet := (dcs >> 2) & 0x03

	isUCS2 := false
	if codingGroup == 0x00 || codingGroup == 0x01 {
		// 一般编码组
		isUCS2 = alphabet == 2 // bit3-2 = 10 -> UCS2
	} else if dcs == 72 {
		isUCS2 = true
	} else if dcs >= 0x40 && dcs <= 0x7F {
		// 消息类编码组
		isUCS2 = alphabet == 2
	}

	if isUCS2 && isHex {
		// UCS2: Hex 字符串 -> UTF-16BE -> UTF-8
		b, err := hex.DecodeString(raw)
		if err != nil {
			logger.Debug("USSD UCS2 hex 解码失败", "err", err, "raw", raw)
			return raw
		}
		if len(b)%2 != 0 {
			return raw
		}
		u16 := make([]uint16, len(b)/2)
		for i := 0; i < len(b); i += 2 {
			u16[i/2] = uint16(b[i])<<8 | uint16(b[i+1])
		}
		return string(utf16.Decode(u16))
	}

	if isHex {
		// 可能是 GSM 7-bit packed 的 hex 表示
		b, err := hex.DecodeString(raw)
		if err != nil {
			return raw
		}
		unpacked := gsm7.Unpack7BitUSSD(b, 0)
		decoded, err := gsm7.Decode(unpacked)
		if err != nil {
			return raw
		}
		return string(decoded)
	}

	// 非 Hex 字符串，直接返回原文（某些 Modem 已经做了解码）
	return raw
}
