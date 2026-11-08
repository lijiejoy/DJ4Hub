package main

import "golang.org/x/sys/unix"

func bindCellularSocket(fd uintptr, interfaceIndex int) error {
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, interfaceIndex)
}
