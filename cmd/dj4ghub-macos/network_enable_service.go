package main

import (
	"fmt"
	"net/http"
)

// enableNetworkService 由上游 main.go（45622dc 一带）移植：启用当前模块对应的 macOS 网络服务。
func (a *app) enableNetworkService(w http.ResponseWriter, _ *http.Request) {
	device := a.currentUSBDevice()
	if device == nil {
		writeError(w, http.StatusConflict, "未检测到兼容 USB 设备")
		return
	}
	service := currentDJINetworkService(discoverMacNetworkServices(), discoverMacNetworkInterfaces(), device.Product)
	if service == nil {
		writeError(w, http.StatusConflict, "未找到当前模块对应的 macOS 网络服务")
		return
	}
	if networkServiceReady(service) {
		writeJSON(w, http.StatusOK, networkServiceRepairResult{
			Ready:          true,
			Summary:        fmt.Sprintf("%s 已经可用", service.Name),
			NetworkService: service,
		})
		return
	}
	if a.demo {
		demoService := *service
		demoService.Disabled = false
		demoService.InterfacePresent = true
		demoService.InterfaceStatus = "active"
		demoService.IPv4 = "192.168.225.23"
		writeJSON(w, http.StatusOK, networkServiceRepairResult{
			Ready:          true,
			Summary:        "演示：网络服务已启用并取得 DHCP 地址",
			NetworkService: &demoService,
		})
		return
	}
	if err := enableMacNetworkService(service.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	service = waitForMacNetworkService(service.Name, device.Product, networkServiceDHCPWait)
	if networkServiceReady(service) {
		writeJSON(w, http.StatusOK, networkServiceRepairResult{
			Ready:          true,
			Summary:        fmt.Sprintf("%s 已启用，已获取 IP %s", service.Name, service.IPv4),
			NetworkService: service,
		})
		return
	}
	writeJSON(w, http.StatusOK, networkServiceRepairResult{
		Ready:          false,
		Summary:        "网络服务已启用，macOS 仍在等待 DHCP 地址",
		NetworkService: service,
	})
}
