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
