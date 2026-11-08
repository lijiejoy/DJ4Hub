package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestLiveModuleNetworkIdentity(t *testing.T) {
	if os.Getenv("DJ4HUB_TEST_NETWORK_IDENTITY") != "1" {
		t.Skip("opt-in read-only network registry check")
	}
	out, err := exec.Command("ioreg", "-a", "-l", "-r", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		t.Fatal(err)
	}
	identities, err := parseModuleNetworkIdentities(out)
	if err != nil || len(identities) == 0 {
		t.Fatalf("module registry mapping: %v, err=%v", identities, err)
	}
	interfaces := discoverMacNetworkInterfaces()
	if got := selectUSBTrafficInterface(interfaces, discoverMacDefaultRoute()); identities[got] == "" {
		t.Fatalf("selected interface %q not in module mapping %v", got, identities)
	}
	t.Logf("verified module interfaces: %v", identities)
}

func TestParseMacInterfaceCountersUsesLinkRow(t *testing.T) {
	input := `Name Mtu Network Address Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll
en9 1500 <Link#22> aa:bb:cc:dd:ee:ff 120 0 4096 80 0 2048 0
en9 1500 192.168.225 192.168.225.20 120 - 4096 80 - 2048 -`

	got := parseMacInterfaceCounters(input)["en9"]
	if got.RX != 4096 || got.TX != 2048 {
		t.Fatalf("en9 counters = %+v, want RX=4096 TX=2048", got)
	}
}

func TestSelectUSBTrafficInterfacePrefersDefaultRoute(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en0", Kind: "ethernet", Status: "active"},
		{Name: "en8", Kind: "ethernet", Status: "active", ModuleID: "module-a"},
		{Name: "en9", Kind: "ethernet", Status: "active", ModuleID: "module-b"},
	}
	if got := selectUSBTrafficInterface(interfaces, macDefaultRoute{Interface: "en9"}); got != "en9" {
		t.Fatalf("selected interface = %q, want en9", got)
	}
}

func TestSelectUSBTrafficInterfaceUsesActiveNonWiFiEthernet(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en0", Kind: "ethernet", Status: "active", IPv4: "192.168.1.29"},
		{Name: "en3", Kind: "ethernet", Status: "inactive"},
		{Name: "en9", Kind: "ethernet", Status: "active", IPv4: "192.168.225.23", ModuleID: "module-a"},
	}
	if got := selectUSBTrafficInterface(interfaces, macDefaultRoute{Interface: "utun14"}); got != "en9" {
		t.Fatalf("selected interface = %q, want en9", got)
	}
}

func TestSelectUSBTrafficInterfaceRequiresModuleIdentity(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en6", Kind: "ethernet", Status: "active"},
		{Name: "en8", Kind: "ethernet", Status: "active", IPv4: "192.168.225.20"},
		{Name: "en0", Kind: "ethernet", Status: "active", ModuleID: "verified-module"},
	}
	if got := selectUSBTrafficInterface(interfaces, macDefaultRoute{Interface: "en8"}); got != "en0" {
		t.Fatalf("selected unrelated default interface: %q", got)
	}
	if got := selectUSBTrafficInterface(interfaces[:2], macDefaultRoute{Interface: "en8"}); got != "" {
		t.Fatalf("guessed a module interface without USB identity: %q", got)
	}
	if hasLikelyUSBNetworkInterface(interfaces[:2]) {
		t.Fatal("unrelated ethernet reported as USB module")
	}
}

func TestParseModuleNetworkIdentities(t *testing.T) {
	input := `<?xml version="1.0"?><plist><array>
<dict><key>IOObjectClass</key><string>IOUSBHostDevice</string>
<key>idVendor</key><integer>11427</integer><key>idProduct</key><integer>16390</integer>
<key>IORegistryEntryID</key><integer>1234</integer>
<key>IORegistryEntryChildren</key><array><dict>
<key>IORegistryEntryChildren</key><array><dict><key>BSD Name</key><string>en15</string></dict></array>
</dict></array></dict>
<dict><key>IOObjectClass</key><string>IOUSBHostDevice</string>
<key>idVendor</key><integer>1452</integer><key>idProduct</key><integer>33331</integer>
<key>IORegistryEntryChildren</key><array><dict><key>BSD Name</key><string>en6</string></dict></array></dict>
</array></plist>`
	got, err := parseModuleNetworkIdentities([]byte(input))
	if err != nil || len(got) != 1 || got["en15"] != "1234" {
		t.Fatalf("USB ancestry mapping = %v, err=%v", got, err)
	}
	if _, err := parseModuleNetworkIdentities([]byte("<plist><array>")); err == nil {
		t.Fatal("invalid registry accepted")
	}
}

func TestSessionTrafficIsDownloadPlusUpload(t *testing.T) {
	rx, tx, total := sessionTrafficFromCounters(
		networkByteCounters{RX: 8192, TX: 4096},
		networkByteCounters{RX: 2048, TX: 1024},
	)
	if rx != 6144 || tx != 3072 || total != 9216 {
		t.Fatalf("session traffic = rx:%d tx:%d total:%d", rx, tx, total)
	}
}

func TestParseNettopActivityTracksProcessAndInterface(t *testing.T) {
	input := `time,,interface,state,bytes_in,bytes_out,
23:01:00,Google Chrome H.976,,,69502,80212,
23:01:00,tcp4 100.65.155.123:54780<->104.18.32.47:443,utun14,Established,4355,28456,
23:01:00,io.tailscale.ip.35082,,,169811,1687128,
23:01:00,tcp4 192.168.225.23:53344<->205.147.105.30:443,en9,Established,7606,21720,
`
	records := parseNettopActivity(input)
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].Process != "Google Chrome H" || records[0].Interface != "utun14" || records[0].IP != "104.18.32.47" || records[0].Port != "443" {
		t.Fatalf("first record = %+v", records[0])
	}
	if records[1].Process != "io.tailscale.ip" || records[1].Interface != "en9" {
		t.Fatalf("second record = %+v", records[1])
	}
}

func TestParseNettopFlowSupportsIPv6(t *testing.T) {
	protocol, host, port := parseNettopFlow("tcp6 2408:1::2.53147<->2606:b740:49::113.80")
	if protocol != "tcp6" || host != "2606:b740:49::113" || port != "80" {
		t.Fatalf("flow = %q %q %q", protocol, host, port)
	}
}
