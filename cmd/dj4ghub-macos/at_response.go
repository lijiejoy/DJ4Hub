package main

import (
	"errors"
	"strings"
)

func validateATCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("AT command is empty")
	}
	for _, char := range command {
		if char < 32 || char == 127 {
			return errors.New("AT command must not contain control characters")
		}
	}
	return nil
}

func atResponseComplete(resp string) bool {
	return atProbeSucceeded(resp) || atResponseIsError(resp)
}

func atResponseIsError(resp string) bool {
	for _, line := range strings.Split(strings.ToUpper(resp), "\n") {
		line = strings.TrimSpace(line)
		if line == "ERROR" || strings.HasPrefix(line, "+CME ERROR:") || strings.HasPrefix(line, "+CMS ERROR:") {
			return true
		}
	}
	return false
}

func atResponseHasPrompt(resp string) bool {
	return strings.HasSuffix(strings.TrimRight(resp, " \t\r\n"), ">")
}

func atProbeSucceeded(resp string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(resp), "\r\n", "\n")
	return normalized == "OK" || strings.HasSuffix(normalized, "\nOK")
}

func normalizeATResponse(resp string) string {
	resp = strings.ReplaceAll(resp, "\r\r\n", "\r\n")
	resp = strings.TrimSpace(resp)
	lines := strings.Split(resp, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\r\n")
}
