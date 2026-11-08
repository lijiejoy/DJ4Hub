package main

import (
	"encoding/xml"
	"os/exec"
)

// Read the registry tree so an interface is attributed to its physical USB
// ancestor rather than inferred from its name, IP address, or route priority.
type registryPlistElement struct {
	XMLName  xml.Name
	Text     string                 `xml:",chardata"`
	Children []registryPlistElement `xml:",any"`
}

func discoverModuleNetworkIdentities() map[string]string {
	out, err := exec.Command("ioreg", "-a", "-l", "-r", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		return nil
	}
	identities, err := parseModuleNetworkIdentities(out)
	if err != nil {
		return nil
	}
	return identities
}

func parseModuleNetworkIdentities(data []byte) (map[string]string, error) {
	var root registryPlistElement
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	identities := make(map[string]string)
	var visit func(registryPlistElement, string)
	visit = func(element registryPlistElement, moduleID string) {
		if element.XMLName.Local != "dict" {
			for _, child := range element.Children {
				visit(child, moduleID)
			}
			return
		}
		properties := make(map[string]registryPlistElement)
		for i := 0; i+1 < len(element.Children); i += 2 {
			if element.Children[i].XMLName.Local == "key" {
				properties[element.Children[i].Text] = element.Children[i+1]
			}
		}
		if properties["IOObjectClass"].Text == "IOUSBHostDevice" {
			moduleID = ""
			if properties["idVendor"].Text == "11427" && properties["idProduct"].Text == "16390" {
				moduleID = properties["IORegistryEntryID"].Text
			}
		}
		if name := properties["BSD Name"].Text; name != "" && moduleID != "" {
			identities[name] = moduleID
		}
		if children, ok := properties["IORegistryEntryChildren"]; ok {
			visit(children, moduleID)
		}
	}
	visit(root, "")
	return identities, nil
}
