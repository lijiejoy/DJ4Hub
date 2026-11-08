package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/backend"
	"github.com/WongLoki/DJ4Hub/internal/config"
	"github.com/WongLoki/DJ4Hub/internal/esim"
	"github.com/WongLoki/DJ4Hub/internal/modem"
	"github.com/WongLoki/DJ4Hub/pkg/smscodec"
)

func main() {
	background, cancelBackground := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelBackground()
	var port string
	var listen string
	var demo bool
	var activate bool
	flag.StringVar(&port, "port", "", "AT serial port; auto-detected when omitted")
	flag.StringVar(&listen, "listen", "127.0.0.1:7575", "HTTP listen address")
	flag.BoolVar(&demo, "demo", false, "run the web UI with simulated modem data")
	flag.BoolVar(&activate, "activate", false, "prepare the DJI USB network interface and exit")
	flag.Parse()

	if activate {
		if err := activateDJINetwork(os.Stdout); err != nil {
			log.Fatalf("activate DJI network: %v", err)
		}
		return
	}

	if demo {
		instance := newDemoApp()
		instance.background = background
		log.Printf("DJ 4G Hub demo mode")
		instance.startSIP()
		serve(instance, listen)
		return
	}

	if strings.TrimSpace(port) == "" {
		var err error
		port, err = discoverATPort()
		if err != nil {
			usbDevice := discoverDJIUSBDevice()
			usbATDevice, usbATErr := openDJIUSBAT()
			instance := &app{
				background:       background,
				port:             "未发现 AT 串口",
				discoveryError:   err.Error(),
				usbDevice:        usbDevice,
				usbAT:            usbATDevice,
				smsPollInterval:  8 * time.Second,
				smsAutoCleanupME: true,
				smsReassembler:   smscodec.NewReassembler(),
			}
			if usbDevice != nil {
				log.Printf("DJI USB device detected without AT serial port: %s %s (%s:%s)",
					usbDevice.Vendor, usbDevice.Product, usbDevice.VendorID, usbDevice.ProductID)
			}
			if usbATErr != nil {
				log.Printf("USB AT unavailable: %v", usbATErr)
			} else {
				instance.port = usbATDevice.Description()
				instance.discoveryError = ""
				defer usbATDevice.Close()
				log.Printf("USB AT bridge opened on DJI %s", usbATDevice.Description())
				instance.initUSBATESIMManager()
			}
			log.Printf("modem discovery skipped: %v", err)
			instance.startSIP()
			serve(instance, listen)
			return
		}
	}

	cfg := config.DeviceConfig{
		ID:            "mac-modem",
		Name:          "DJI 4G Module",
		ATPort:        port,
		ManagePort:    port,
		DeviceBackend: backend.BackendAT,
		ESIMTransport: "at",
		BaudRate:      115200,
		DataBits:      8,
		StopBits:      1,
		Parity:        "none",
		SMSEnabled:    true,
	}
	manager, err := modem.New(cfg)
	if err != nil {
		log.Fatalf("create modem manager: %v", err)
	}

	instance := &app{background: background, modem: manager, port: port, smsPollInterval: 8 * time.Second, smsAutoCleanupME: true}
	manager.SetSMSCallback(instance.recordSMS)
	manager.SetDeliveryReportCallback(instance.recordDeliveryReport)
	if err := manager.Start(); err != nil {
		log.Fatalf("open modem on %s: %v", port, err)
	}
	defer manager.Stop()

	if !manager.WaitReady(15 * time.Second) {
		log.Printf("modem initialization is still running; the web UI will remain available")
	}

	atBackend := backend.NewATBackend(manager)
	esimManager, err := esim.NewManager(esim.ManagerOptions{
		DeviceID:  "mac-modem",
		Transport: "at",
		Modem:     manager,
		Backend:   atBackend,
	})
	if err != nil {
		log.Printf("eSIM manager unavailable: %v", err)
	} else {
		instance.installESIMManager(esimManager, false)
	}

	go manager.CheckAllSMS()

	instance.startSIP()
	serve(instance, listen)
}

func serve(instance *app, listen string) {
	instance.alertsStarted = time.Now()
	if err := instance.initHistory(); err != nil {
		log.Fatalf("open communication history: %v", err)
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           localRequestPolicy(listen, instance.routes()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go instance.monitorCallHistory(ctx)
	go instance.monitorHistoryBackup(ctx)

	if !instance.demo {
		log.Printf("DJ 4G Hub is using %s", instance.port)
	}
	log.Printf("Open http://%s", listen)
	instance.startTaskRunner()
	instance.startMCP()
	instance.startNotifier()
	if !instance.demo && instance.modem == nil {
		go instance.startSMSPoller(ctx)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server stopped unexpectedly: %v", err)
		}
	case <-ctx.Done():
		log.Printf("DJ 4G Hub is stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown: %v", err)
		}
		instance.stopSIP()
	}
}

func newDemoApp() *app {
	now := time.Now()
	return &app{
		demo:            true,
		port:            "Demo · Quectel EG25-G",
		smsPollInterval: 8 * time.Second,
		sms: []receivedSMS{
			{
				Sender:    "10086",
				Content:   "【DJ 4G Hub 演示】本月套餐剩余流量 18.6GB。",
				Timestamp: now.Add(-18 * time.Minute),
			},
			{
				Sender:    "+44 7400 123456",
				Content:   "Your verification code is 482913. It expires in 10 minutes.",
				Code:      "482913",
				Timestamp: now.Add(-2 * time.Hour),
			},
		},
	}
}
