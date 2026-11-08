package subscription

import "testing"

func TestDeriveSubscriptionID(t *testing.T) {
	tests := []struct {
		name      string
		iccid     string
		eid       string
		profileID string
		want      string
	}{
		{"iccid wins", "89860123456789012345", "89049032000001000000113509931049", "", "iccid:89860123456789012345"},
		{"eid plus profile", "", "89049032000001000000113509931049", "profile-1", "eicc:89049032000001000000113509931049:profile-1"},
		{"eid only", "", "89049032000001000000113509931049", "", "eicc:89049032000001000000113509931049"},
		{"nothing known", "", "", "", "unknown"},
		{"whitespace iccid ignored", "  ", "89049032000001000000113509931049", "", "eicc:89049032000001000000113509931049"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveSubscriptionID(tt.iccid, tt.eid, tt.profileID); got != tt.want {
				t.Fatalf("DeriveSubscriptionID()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestNumberSourceLabel(t *testing.T) {
	if NumberSourceAuto.Label() == NumberSourceRemark.Label() {
		t.Fatalf("auto and remark labels must differ")
	}
	if NumberSourceUnknown.Label() != "未知" {
		t.Fatalf("unknown label=%q want 未知", NumberSourceUnknown.Label())
	}
}

func TestMaskICCID(t *testing.T) {
	if got := MaskICCID("89860123456789012345"); got != "8986 •••••••••••• 2345" {
		t.Fatalf("MaskICCID()=%q", got)
	}
	if got := MaskICCID(""); got != "" {
		t.Fatalf("MaskICCID(empty)=%q want empty", got)
	}
	if got := MaskICCID("12345678"); got != "••••••••" {
		t.Fatalf("MaskICCID(short)=%q want all masked", got)
	}
}

func TestMaskNumber(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"+8613800138000", "+8613*****8000"},
		{"13800138000", "1380***8000"},
		{"10086", "10086"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := MaskNumber(tt.in); got != tt.want {
			t.Fatalf("MaskNumber(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeRemarkNumber(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"+86 138 0013 8000", "+8613800138000", true},
		{"138-0013-8000", "13800138000", true},
		{"(138)00138000", "13800138000", true},
		{"10086", "", false},              // 少于 6 位
		{"abc", "", false},                // 非数字
		{"138001380001234567", "", false}, // 多于 15 位
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizeRemarkNumber(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("NormalizeRemarkNumber(%q)=(%q,%v) want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIdentitySnapshotKeepsEventTimeIdentity(t *testing.T) {
	id := Identity{
		SubscriptionID: "iccid:89860123456789012345",
		ICCID:          "89860123456789012345",
		IMEI:           "867400000000001",
		EID:            "89049032000001000000113509931049",
		Number:         "+8613800138000",
		NumberSource:   NumberSourceAuto,
	}
	snap := id.Snapshot()
	if snap.SubscriptionID != id.SubscriptionID || snap.ICCID != id.ICCID ||
		snap.IMEI != id.IMEI || snap.EID != id.EID ||
		snap.Number != id.Number || snap.NumberSource != id.NumberSource {
		t.Fatalf("snapshot lost identity fields: %#v", snap)
	}
	if snap.CapturedAt.IsZero() {
		t.Fatalf("snapshot has no capture time")
	}
}
