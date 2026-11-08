package modem

import (
	"testing"
)

func TestBuildQNWLockCommand(t *testing.T) {
	cmd, err := BuildQNWLockCommand(3740, 132)
	if err != nil {
		t.Fatalf("BuildQNWLockCommand() error = %v", err)
	}
	if want := `AT+QNWLOCK="common/lte",2,3740,132`; cmd != want {
		t.Fatalf("BuildQNWLockCommand() = %q, want %q", cmd, want)
	}
	for _, tc := range []struct{ earfcn, pci int }{
		{-1, 0}, {262144, 0}, {0, -1}, {0, 504},
	} {
		if _, err := BuildQNWLockCommand(tc.earfcn, tc.pci); err == nil {
			t.Fatalf("BuildQNWLockCommand(%d, %d) error = nil, want error", tc.earfcn, tc.pci)
		}
	}
	if got := BuildQNWUnlockCommand(); got != `AT+QNWLOCK="common/lte",0` {
		t.Fatalf("BuildQNWUnlockCommand() = %q", got)
	}
}

func TestParseQNWLockResponse(t *testing.T) {
	cases := []struct {
		name   string
		resp   string
		want   CellLockState
		wantOK bool
	}{
		{
			name:   "locked with earfcn and pci",
			resp:   "\r\n+QNWLOCK: \"common/lte\",2,3740,132,1\r\n\r\nOK\r\n",
			want:   CellLockState{Locked: true, EARFCN: 3740, PCI: 132, Supported: true},
			wantOK: true,
		},
		{
			name:   "unlocked action 0",
			resp:   "\r\n+QNWLOCK: \"common/lte\",0\r\n\r\nOK\r\n",
			want:   CellLockState{Locked: false, Supported: true},
			wantOK: true,
		},
		{
			name:   "bare mode line means no lock",
			resp:   "\r\n+QNWLOCK: \"common/lte\"\r\n\r\nOK\r\n",
			want:   CellLockState{Locked: false, Supported: true},
			wantOK: true,
		},
		{
			name:   "no qnwlock line",
			resp:   "\r\nOK\r\n",
			wantOK: false,
		},
		{
			name:   "error response",
			resp:   "\r\nERROR\r\n",
			wantOK: false,
		},
		{
			name:   "malformed action",
			resp:   "\r\n+QNWLOCK: \"common/lte\",x,3740,132\r\n\r\nOK\r\n",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseQNWLockResponse(tc.resp)
			if ok != tc.wantOK {
				t.Fatalf("ParseQNWLockResponse() ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Locked != tc.want.Locked || got.EARFCN != tc.want.EARFCN || got.PCI != tc.want.PCI || got.Supported != tc.want.Supported {
				t.Fatalf("ParseQNWLockResponse() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseServingCellLTEInfoIncludesPCIAndEARFCN(t *testing.T) {
	info, ok := parseServingCellLTEInfo("\r\n+QENG: \"servingcell\",\"NOCONN\",\"LTE\",\"FDD\",460,01,8401A29,132,3740,8,3,3,-95,5992,-75,-8,-50,11,44\r\n\r\nOK\r\n")
	if !ok {
		t.Fatal("parseServingCellLTEInfo() ok=false")
	}
	if info.PCI != 132 || info.EARFCN != 3740 || info.Channel != 3740 || info.CellID != "8401A29" {
		t.Fatalf("parseServingCellLTEInfo()=%+v, want PCI 132 EARFCN 3740 CellID 8401A29", info)
	}
}

func TestBuildCUSDCommand(t *testing.T) {
	cmd, err := BuildCUSDCommand("*100#")
	if err != nil {
		t.Fatalf("BuildCUSDCommand() error = %v", err)
	}
	if want := `AT+CUSD=1,"*100#",15`; cmd != want {
		t.Fatalf("BuildCUSDCommand() = %q, want %q", cmd, want)
	}
	for _, bad := range []string{"", "   ", `1"2`, "1\x012", "1\n2"} {
		if _, err := BuildCUSDCommand(bad); err == nil {
			t.Fatalf("BuildCUSDCommand(%q) error = nil, want error", bad)
		}
	}
	long := make([]byte, 183)
	for i := range long {
		long[i] = '1'
	}
	if _, err := BuildCUSDCommand(string(long)); err == nil {
		t.Fatal("BuildCUSDCommand(183 chars) error = nil, want error")
	}
}

func TestParseCUSDLine(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		status int
		dcs    int
		text   string
		ok     bool
	}{
		{"plain", `+CUSD: 1,"Balance: 10",15`, 1, 15, "Balance: 10", true},
		{"comma in quoted text", `+CUSD: 1,"1. Balance, 2. Data",15`, 1, 15, "1. Balance, 2. Data", true},
		{"no dcs", `+CUSD: 2,"Done"`, 2, -1, "Done", true},
		{"unquoted text", `+CUSD: 0,Hello,15`, 0, 15, "Hello", true},
		{"ucs2 hex", `+CUSD: 1,"4F60597D",72`, 1, 72, "4F60597D", true},
		{"not cusd", "+CMTI: \"SM\",3", 0, 0, "", false},
		{"missing text", "+CUSD: 1", 0, 0, "", false},
		{"bad status", `+CUSD: x,"hi",15`, 0, 0, "", false},
		{"unterminated quote", `+CUSD: 1,"hi`, 0, 0, "", false},
		{"empty", "", 0, 0, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, dcs, text, ok := ParseCUSDLine(tc.line)
			if ok != tc.ok || status != tc.status || dcs != tc.dcs || text != tc.text {
				t.Fatalf("ParseCUSDLine(%q) = (%d, %d, %q, %v), want (%d, %d, %q, %v)",
					tc.line, status, dcs, text, ok, tc.status, tc.dcs, tc.text, tc.ok)
			}
		})
	}
}

func TestDecodeUSSDTextUCS2(t *testing.T) {
	// "你好" in UCS2 hex.
	if got := DecodeUSSDText("4F60597D", 72); got != "你好" {
		t.Fatalf("DecodeUSSDText(UCS2) = %q, want 你好", got)
	}
	if got := DecodeUSSDText("plain text", 15); got != "plain text" {
		t.Fatalf("DecodeUSSDText(plain) = %q", got)
	}
	if got := DecodeUSSDText("", 15); got != "" {
		t.Fatalf("DecodeUSSDText(empty) = %q", got)
	}
}

func TestParseCMGSRef(t *testing.T) {
	cases := []struct {
		resp string
		want int
	}{
		{"+CMGS: 42\r\nOK", 42},
		{"AT+CMGS=10\r\n+CMGS: 7\r\n\r\nOK", 7},
		{"OK", -1},
		{"+CMGS: abc", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := parseCMGSRef(tc.resp); got != tc.want {
			t.Fatalf("parseCMGSRef(%q) = %d, want %d", tc.resp, got, tc.want)
		}
	}
}
