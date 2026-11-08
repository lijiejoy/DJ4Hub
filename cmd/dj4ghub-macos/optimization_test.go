package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/backend"
	"github.com/WongLoki/DJ4Hub/internal/sip"
)

func TestOptimizationOperatorReadsNeverStartScan(t *testing.T) {
	a := offlineTestApp()
	h := a.routes()
	for _, path := range []string{"/api/radio/operators", "/api/radio/operators"} {
		rec := doJSON(t, h, "GET", path, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"idle"`) {
			t.Fatalf("cache read should work without touching modem: %d %s", rec.Code, rec.Body.String())
		}
	}
	expired := time.Now().Add(-time.Hour)
	a.operatorScan = &operatorScanState{State: "done", FinishedAt: &expired,
		Operators: []backend.OperatorCandidate{{PLMN: "46000"}}}
	rec := doJSON(t, h, "GET", "/api/radio/operators", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"46000"`) {
		t.Fatalf("expired results must remain readable until explicit scan: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOptimizationScanRejectsIncomingCall(t *testing.T) {
	a := offlineTestApp()
	a.sip = sip.New(sip.DefaultConfig(), nil)
	a.sip.IncomingCall("12345")
	defer a.sip.Stop()
	rec := doJSON(t, a.routes(), "POST", "/api/radio/operators/scan", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("ringing must reject long scan before device access: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOptimizationUSSDStateConsistentAcrossClients(t *testing.T) {
	a := offlineTestApp()
	for _, state := range []string{"idle", "active", "ended", "cancelled", "error"} {
		a.ussdSession = nil
		if state != "idle" {
			a.storeUSSDSession(&ussdSession{ID: "test", State: state})
		}
		result, err := a.mcpCallReadTool("ussd_status")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		rec := doJSON(t, a.routes(), "GET", "/api/ussd/status", "")
		var mcp, httpResult map[string]any
		_ = json.Unmarshal(raw, &mcp)
		_ = json.Unmarshal(rec.Body.Bytes(), &httpResult)
		if mcp["state"] != state || mcp["state"] != httpResult["state"] {
			t.Errorf("%s: MCP=%s HTTP=%s", state, raw, rec.Body.String())
		}
	}
}

func TestOptimizationGNSSRejectsInvalidCoordinates(t *testing.T) {
	for _, value := range []string{"NaNN", "InfE", "-100N", "3160.1N", "9100N", "18100E", "9000.1S", "123X"} {
		if coord, err := parseNMEACoord(value); err == nil {
			t.Errorf("bad coordinate %q accepted: %v", value, coord)
		}
	}
	for _, coordinates := range []string{"12121.0E,3113.0N", "NaNN,12121.0E"} {
		st, _ := parseQGPSLOC("+QGPSLOC: 085959.0," + coordinates + ",NaN,Inf,3,0,NaN,0,071126,8")
		if st.Fix {
			t.Errorf("invalid fix accepted: %+v", st)
		}
		if _, err := json.Marshal(st); err != nil {
			t.Errorf("bad floats must not break API encoding: %v", err)
		}
	}
}

func TestOptimizationEmailHonorsContextDuringGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			// The old smtp.Dial ignores cancellation and waits for this close.
			time.Sleep(400 * time.Millisecond)
		}
	}()
	cfg := emailNotifySettings{SMTPHost: "127.0.0.1", SMTPPort: listener.Addr().(*net.TCPAddr).Port}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = sendEmail(ctx, cfg, "test", "test")
	elapsed := time.Since(start)
	<-done
	if err == nil || elapsed > 250*time.Millisecond {
		t.Fatalf("SMTP greeting must honor deadline: %v in %v", err, elapsed)
	}
}

func TestOptimizationEmailCancellationWithoutDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		cancel()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, conn)
	}()
	cfg := emailNotifySettings{SMTPHost: "127.0.0.1", SMTPPort: listener.Addr().(*net.TCPAddr).Port}
	start := time.Now()
	err = sendEmail(ctx, cfg, "test", "test")
	<-done
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancellation did not terminate SMTP greeting: %v", err)
	}
}

func TestOptimizationEmailRefusesAuthenticationWithoutTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			commands <- ""
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(conn, "220 local test\r\n")
		reader := bufio.NewReader(conn)
		hello, _ := reader.ReadString('\n')
		_, _ = io.WriteString(conn, "250 local test\r\n")
		remaining, _ := io.ReadAll(reader)
		commands <- hello + string(remaining)
	}()
	cfg := emailNotifySettings{SMTPHost: "127.0.0.1", SMTPPort: listener.Addr().(*net.TCPAddr).Port,
		Username: "test", Password: "FAKE-SECRET"}
	err = sendEmail(context.Background(), cfg, "test", "test")
	received := <-commands
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("authenticated SMTP accepted plaintext: %v", err)
	}
	if strings.Contains(received, "AUTH") || strings.Contains(received, "MAIL") || strings.Contains(received, "FAKE-SECRET") {
		t.Fatalf("sent credentials or message before TLS: %q", received)
	}
}

func TestOptimizationNotificationErrorsDoNotExposeRemoteText(t *testing.T) {
	const secret = "FAKE-CREDENTIAL-AND-MESSAGE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(secret + r.URL.String()))
	}))
	defer srv.Close()
	err := sendTelegram(context.Background(), telegramNotifySettings{BotToken: secret, BaseURL: srv.URL}, "test")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("remote echo must not enter errors: %v", err)
	}
	a := &app{}
	a.recordNotifyResult("telegram", "test", "test", errors.New("Get https://example.invalid/bot"+secret))
	if strings.Contains(a.notifyLog[0].Error, secret) || strings.Contains(a.notifyStatus["telegram"].LastError, secret) {
		t.Fatal("notification result exposed a URL credential")
	}
}

func TestOptimizationPushBusinessErrorsAreFailures(t *testing.T) {
	senders := map[string]func(string) error{
		"telegram": func(endpoint string) error {
			return sendTelegram(context.Background(), telegramNotifySettings{BaseURL: endpoint}, "test")
		},
		"feishu": func(endpoint string) error {
			return sendFeishu(context.Background(), feishuNotifySettings{WebhookURL: endpoint}, "test")
		},
		"bark": func(endpoint string) error {
			return sendBark(context.Background(), barkNotifySettings{URLs: []string{endpoint}}, "test", "test")
		},
		"pushplus": func(endpoint string) error {
			old := pushplusEndpoint
			pushplusEndpoint = endpoint
			defer func() { pushplusEndpoint = old }()
			return sendPushplus(context.Background(), pushplusNotifySettings{}, "test", "test")
		},
	}
	for channel, send := range senders {
		for _, response := range []string{`{}`, `{"ok":false,"code":400,"StatusCode":1,"message":"FAKE-SECRET"}`} {
			t.Run(channel+"/"+response, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, response)
				}))
				defer server.Close()
				err := send(server.URL)
				if err == nil || strings.Contains(err.Error(), "FAKE-SECRET") {
					t.Fatalf("HTTP 200 business failure was accepted or leaked: %v", err)
				}
			})
		}
	}
}

func TestOptimizationWebhookDoesNotHideEarlierTargetFailure(t *testing.T) {
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer failed.Close()
	passed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer passed.Close()
	err := sendWebhook(context.Background(), webhookNotifySettings{URLs: []string{failed.URL, passed.URL}},
		notifyEvent{Kind: "test"})
	if err == nil {
		t.Fatal("last successful target hid another failed delivery")
	}
}

func TestOptimizationTaskSaveFailureDoesNotChangeMemory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &app{demo: true, tasks: []*scheduledTask{}}
	path := a.tasksPath()
	if err := os.MkdirAll(filepath.Join(path+".tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, a.routes(), "POST", "/api/tasks", `{"kind":"reboot","interval_m":5}`)
	if rec.Code != 500 || len(a.getTasks()) != 0 {
		t.Fatalf("failed task creation left a runnable task: HTTP %d, tasks=%d", rec.Code, len(a.getTasks()))
	}
}

func TestOptimizationTaskMutationsRollbackOnSaveFailure(t *testing.T) {
	for _, operation := range []string{"enable", "delete", "run-now"} {
		t.Run(operation, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			task := &scheduledTask{ID: "existing", Kind: "reboot", Enabled: false, IntervalM: 5}
			a := &app{demo: true, tasks: []*scheduledTask{task}}
			if err := os.MkdirAll(a.tasksPath()+".tmp", 0700); err != nil {
				t.Fatal(err)
			}
			body := `{"id":"existing"}`
			if operation == "enable" {
				body = `{"id":"existing","enabled":true}`
			}
			rec := doJSON(t, a.routes(), "POST", "/api/tasks/"+operation, body)
			if rec.Code != 500 || len(a.getTasks()) != 1 || task.Enabled || task.Running {
				t.Fatalf("failed %s mutated runnable state: HTTP %d task=%+v", operation, rec.Code, task)
			}
		})
	}
}

func TestOptimizationInterruptedTaskIsPaused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &app{demo: true, background: t.Context(), tasks: []*scheduledTask{{
		ID: "interrupted", Kind: "sms", To: "10086", Text: "test", Enabled: true,
		Running: true, IntervalM: 5, NextRunAt: time.Now().Add(-time.Hour),
	}}}
	a.startTaskRunner()
	task := a.getTasks()[0]
	if task.Enabled || task.Running || task.LastResult == "" {
		t.Fatalf("uncertain previous action must not run again automatically: %+v", task)
	}
}

func TestOptimizationDailyTaskKeepsLocalTimeAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 8, 30, 0, 0, zone)
	next := nextRunAfter(now, 0, "08:30")
	if next.Day() != 1 || next.Hour() != 8 || next.Minute() != 30 {
		t.Fatalf("daily task shifted local time across DST: %v", next)
	}
}

func TestOptimizationATRejectsBareErrorAndInjectedCommands(t *testing.T) {
	if !atResponseIsError("ERROR") || !atResponseComplete("ERROR") {
		t.Error("bare modem rejection was mistaken for success")
	}
	a := &app{demo: true}
	for _, command := range []string{"AT\rATH", "AT\nAT+CFUN=1,1", "AT\x00", "AT\x1a"} {
		if _, err := a.runATCommand(command, time.Second); err == nil {
			t.Errorf("control characters accepted in one AT command: %q", command)
		}
	}
}

func TestOptimizationIncomingNotificationUsesCallIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &app{}
	a.sip = sip.New(sip.DefaultConfig(), nil)
	a.sip.IncomingCall("")
	firstID := a.sip.Calls()[0].ID
	a.notifyIncomingCall("")
	a.sip.IncomingCall("12345")
	a.notifyIncomingCall("12345")
	a.sip.ModemHungUp()
	a.sip.IncomingCall("12345")
	a.notifyIncomingCall("12345")
	defer a.sip.Stop()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.notifyLogMu.Lock()
		count := len(a.notifyLog)
		a.notifyLogMu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.notifyRingMu.Lock()
	keys := len(a.notifyRingLast)
	_, hasCallID := a.notifyRingLast[firstID]
	a.notifyRingMu.Unlock()
	a.notifyLogMu.Lock()
	count := len(a.notifyLog)
	a.notifyLogMu.Unlock()
	if keys != 2 || count != 2 || !hasCallID {
		t.Fatalf("late number must not push twice and second call must push: keys=%d notifications=%d", keys, count)
	}
}

func TestOptimizationLocalRequestPolicy(t *testing.T) {
	handler := localRequestPolicy("127.0.0.1:7575", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	for _, test := range []struct {
		host, origin, site, method string
		want                       int
	}{
		{"127.0.0.1:7575", "", "", "GET", 204},
		{"localhost:7575", "http://localhost:7575", "same-origin", "POST", 204},
		{"127.0.0.1:7575", "http://127.0.0.1:7575", "", "POST", 204},
		{"rebind.example:7575", "", "", "GET", 403},
		{"127.0.0.1:7575", "https://attacker.example", "", "GET", 403},
		{"127.0.0.1:7575", "null", "", "POST", 403},
		{"127.0.0.1:7575", "", "cross-site", "POST", 403},
		{"127.0.0.1:9999", "", "", "GET", 403},
	} {
		req := httptest.NewRequest(test.method, "http://"+test.host+"/api/sip/status", nil)
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Sec-Fetch-Site", test.site)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%+v: status=%d", test, rec.Code)
		}
	}
}

func TestOptimizationCancelledRunnerDoesNotExecuteTasks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &app{demo: true, background: ctx, tasks: []*scheduledTask{{
		ID: "cancelled", Kind: "reboot", Enabled: true, IntervalM: 5,
		NextRunAt: time.Now().Add(-time.Hour),
	}}}
	a.runDueTasks()
	if task := a.getTasks()[0]; task.RunCount != 0 || task.Running {
		t.Fatalf("shutdown runner executed a task: %+v", task)
	}
}

func TestOptimizationNativeRingingOwnershipClearsOnFailure(t *testing.T) {
	alerts := &localCallAlerts{
		sound: func(ctx context.Context) { <-ctx.Done() },
		dialog: func(context.Context, string, <-chan string) (string, error) {
			return "", errors.New("window unavailable")
		},
	}
	defer alerts.Close()
	alerts.OnCallState(sip.PublicCall{ID: "incoming", Direction: "inbound", State: sip.CallStateRinging})
	deadline := time.Now().Add(time.Second)
	for alerts.RingingCallID() != "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if alerts.RingingCallID() != "" {
		t.Fatal("native failure prevented the browser from taking over ringing")
	}
}

func TestOptimizationConfirmationDoesNotFallBackToFixedCode(t *testing.T) {
	code, err := confirmCodeFrom(strings.NewReader(""))
	if err == nil || code != "" {
		t.Fatal("entropy failure must stop the confirmation flow")
	}
	code, err = newConfirmCode()
	if err != nil || len(code) != 6 {
		t.Fatalf("normal confirmation generation failed: %v", err)
	}
}
