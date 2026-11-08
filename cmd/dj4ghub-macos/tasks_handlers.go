package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/modem"
)

// Scheduled automatic tasks (Item 4).
//
// A task is a small job the modem runs on its own: a USSD query (e.g. a
// balance check), an SMS send, or a module reboot. Tasks persist in
// tasks.json next to the SIP config (0600, same pattern as radio-prefs.json)
// and survive restarts; a task that is due while the app is down runs on
// the next tick after startup. An interrupted execution is paused because
// the hardware action may have succeeded before its result was persisted.
//
// Kinds:
//   - "ussd":   Code is the USSD string, e.g. "*100#".
//   - "sms":    To + Text; sent with the normal PDU path (delivery report
//     requested, visible under /api/sms/delivery).
//   - "reboot": AT+CFUN=1,1; drops the data link for ~30 s while the
//     module restarts. Payload is ignored.
//
// Schedule: either IntervalM (every N minutes, N >= 5) or Daily "HH:MM"
// (once a day, local time). Exactly one of the two must be set.
// The runner ticks every 30 s and runs due tasks serially so AT commands
// never overlap; a task whose previous run is still going is not started
// again. Runs are recorded on the task (last run, result, run count).

const tasksFileName = "tasks.json"

const taskTickInterval = 30 * time.Second

type scheduledTask struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Code       string     `json:"code,omitempty"`
	To         string     `json:"to,omitempty"`
	Text       string     `json:"text,omitempty"`
	IntervalM  int        `json:"interval_m,omitempty"`
	Daily      string     `json:"daily,omitempty"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	NextRunAt  time.Time  `json:"next_run_at"`
	LastResult string     `json:"last_result,omitempty"`
	LastOK     bool       `json:"last_ok"`
	RunCount   int        `json:"run_count"`
	Running    bool       `json:"running"`
}

// --- persistence ---------------------------------------------------------

func (a *app) tasksPath() string {
	dir, err := a.sipConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, tasksFileName)
}

func (a *app) loadTasksLocked() []*scheduledTask {
	path := a.tasksPath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var tasks []*scheduledTask
	if err := json.Unmarshal(raw, &tasks); err != nil {
		log.Printf("[tasks] %s is corrupt, starting with no tasks: %v", path, err)
		return nil
	}
	return tasks
}

func (a *app) saveTasksLocked(tasks []*scheduledTask) error {
	path := a.tasksPath()
	if path == "" {
		return errors.New("no config directory for tasks")
	}
	raw, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *app) getTasks() []*scheduledTask {
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	if a.tasks == nil {
		a.tasks = a.loadTasksLocked()
	}
	out := make([]*scheduledTask, len(a.tasks))
	for i, t := range a.tasks {
		cp := *t
		out[i] = &cp
	}
	return out
}

func (a *app) findTaskLocked(id string) *scheduledTask {
	for _, t := range a.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// --- scheduling ----------------------------------------------------------

func nextRunAfter(now time.Time, intervalM int, daily string) time.Time {
	if daily != "" {
		hh, mm, err := parseHHMM(daily)
		if err == nil {
			next := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
			if !next.After(now) {
				next = time.Date(now.Year(), now.Month(), now.Day()+1, hh, mm, 0, 0, now.Location())
			}
			return next
		}
	}
	if intervalM > 0 {
		return now.Add(time.Duration(intervalM) * time.Minute)
	}
	return now
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("daily must be HH:MM")
	}
	hh, err := strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 {
		return 0, 0, fmt.Errorf("daily hour must be 00-23")
	}
	mm, err := strconv.Atoi(parts[1])
	if err != nil || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("daily minute must be 00-59")
	}
	return hh, mm, nil
}

func validateTaskBody(kind, code, to, text string, intervalM int, daily string) error {
	switch kind {
	case "ussd":
		if _, err := modem.BuildCUSDCommand(code); err != nil {
			return err
		}
	case "sms":
		if strings.TrimSpace(to) == "" || strings.TrimSpace(text) == "" {
			return errors.New("sms tasks need to and text")
		}
	case "reboot":
	default:
		return fmt.Errorf("kind must be ussd, sms or reboot")
	}
	if intervalM > 0 && daily != "" {
		return errors.New("set interval_m or daily, not both")
	}
	if intervalM == 0 && daily == "" {
		return errors.New("set interval_m (minutes, >= 5) or daily HH:MM")
	}
	if intervalM != 0 && intervalM < 5 {
		return errors.New("interval_m must be >= 5")
	}
	if daily != "" {
		if _, _, err := parseHHMM(daily); err != nil {
			return err
		}
	}
	return nil
}

// startTaskRunner loads persisted tasks and ticks every 30 s. It is
// started from serve() so every launch path (serial, USB-AT, demo) gets it.
func (a *app) startTaskRunner() {
	a.tasksMu.Lock()
	if a.tasks == nil {
		a.tasks = a.loadTasksLocked()
	}
	now := time.Now()
	for _, t := range a.tasks {
		if t.NextRunAt.IsZero() {
			t.NextRunAt = nextRunAfter(now, t.IntervalM, t.Daily)
		}
		if t.Running {
			t.Running = false
			t.Enabled = false
			t.LastOK = false
			t.LastResult = "上次执行被中断，结果不确定；已暂停，请确认后再启用"
		}
	}
	if err := a.saveTasksLocked(a.tasks); err != nil {
		log.Printf("[tasks] cannot persist startup recovery; runner paused: %v", err)
		a.tasksMu.Unlock()
		return
	}
	a.tasksMu.Unlock()
	go a.taskTickLoop()
}

func (a *app) taskTickLoop() {
	ticker := time.NewTicker(taskTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.backgroundContext().Done():
			return
		case <-ticker.C:
			a.runDueTasks()
		}
	}
}

func (a *app) runDueTasks() {
	if a.backgroundContext().Err() != nil {
		return
	}
	a.tasksMu.Lock()
	now := time.Now()
	var due []*scheduledTask
	for _, t := range a.tasks {
		if t.Enabled && !t.Running && !t.NextRunAt.After(now) {
			t.Running = true
			due = append(due, t)
		}
	}
	if len(due) > 0 {
		if err := a.saveTasksLocked(a.tasks); err != nil {
			for _, t := range due {
				t.Running = false
			}
			a.tasksMu.Unlock()
			log.Printf("[tasks] execution intent could not be saved; no task executed: %v", err)
			return
		}
	}
	a.tasksMu.Unlock()
	for _, t := range due {
		a.runTask(t)
	}
}

// runTask executes one task and records the outcome. It runs on the
// ticker's goroutine, serially, so AT traffic never overlaps.
func (a *app) runTask(t *scheduledTask) {
	start := time.Now()
	result, ok := a.executeTask(t)
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	t.Running = false
	t.LastRunAt = &start
	t.LastResult = result
	t.LastOK = ok
	t.RunCount++
	t.UpdatedAt = time.Now()
	t.NextRunAt = nextRunAfter(time.Now(), t.IntervalM, t.Daily)
	if err := a.saveTasksLocked(a.tasks); err != nil {
		t.Enabled = false
		t.LastOK = false
		t.LastResult = "执行结果保存失败，已暂停；结果不确定，请检查后再启用"
		log.Printf("[tasks] result could not be saved; task paused: %v", err)
	}
	log.Printf("[tasks] %s (%s) -> ok=%v: %s", t.Name, t.Kind, ok, result)
}

func (a *app) executeTask(t *scheduledTask) (string, bool) {
	if err := a.backgroundContext().Err(); err != nil {
		return "服务正在停止，任务未执行", false
	}
	if a.demo {
		return "skipped: demo mode, no modem", false
	}
	if a.hasLiveCall() {
		return "正在通话或有来电，任务未执行", false
	}
	switch t.Kind {
	case "ussd":
		if err := a.requireModemTransport(); err != nil {
			return "modem unavailable: " + err.Error(), false
		}
		res, err := a.execUSSD(t.Code, 30*time.Second)
		if err != nil {
			return err.Error(), false
		}
		if strings.TrimSpace(res.Text) == "" {
			return "USSD ok, empty reply", true
		}
		return "USSD: " + res.Text, true
	case "sms":
		if a.demo {
			return "skipped: demo mode", false
		}
		segments, mrs, err := a.sendTextSMS(t.To, t.Text)
		if err != nil {
			return err.Error(), false
		}
		a.recordSentSMS(t.To, mrs)
		return fmt.Sprintf("SMS sent: %d segment(s), refs %v", segments, mrs), true
	case "reboot":
		resp, err := a.runATCommand("AT+CFUN=1,1", 3*time.Second)
		if err != nil {
			return err.Error(), false
		}
		if atResponseIsError(resp) {
			return "modem rejected reboot: " + strings.TrimSpace(resp), false
		}
		return "reboot accepted, module restarting", true
	default:
		return "unknown kind " + t.Kind, false
	}
}

// --- HTTP handlers -------------------------------------------------------

func (a *app) listTasks(w http.ResponseWriter, _ *http.Request) {
	tasks := a.getTasks()
	if tasks == nil {
		tasks = []*scheduledTask{}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	writeJSON(w, http.StatusOK, tasks)
}

func (a *app) createTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Code      string `json:"code"`
		To        string `json:"to"`
		Text      string `json:"text"`
		IntervalM int    `json:"interval_m"`
		Daily     string `json:"daily"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	code := strings.TrimSpace(body.Code)
	to := strings.TrimSpace(body.To)
	text := strings.TrimSpace(body.Text)
	daily := strings.TrimSpace(body.Daily)
	if err := validateTaskBody(kind, code, to, text, body.IntervalM, daily); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now()
	t := &scheduledTask{
		ID:        fmt.Sprintf("task-%d", now.UnixMilli()),
		Name:      strings.TrimSpace(body.Name),
		Kind:      kind,
		Code:      code,
		To:        to,
		Text:      text,
		IntervalM: body.IntervalM,
		Daily:     daily,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
		NextRunAt: nextRunAfter(now, body.IntervalM, daily),
	}
	if t.Name == "" {
		t.Name = kind + " " + t.ID
	}
	a.tasksMu.Lock()
	if a.tasks == nil {
		a.tasks = a.loadTasksLocked()
	}
	next := append(append([]*scheduledTask(nil), a.tasks...), t)
	err := a.saveTasksLocked(next)
	if err == nil {
		a.tasks = next
	}
	a.tasksMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *app) setTaskEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	if a.tasks == nil {
		a.tasks = a.loadTasksLocked()
	}
	t := a.findTaskLocked(body.ID)
	if t == nil {
		writeError(w, http.StatusNotFound, "no such task")
		return
	}
	if t.Running {
		writeError(w, http.StatusConflict, "task is already running")
		return
	}
	previous := *t
	t.Enabled = body.Enabled
	t.UpdatedAt = time.Now()
	if body.Enabled && t.NextRunAt.Before(time.Now()) {
		t.NextRunAt = nextRunAfter(time.Now(), t.IntervalM, t.Daily)
	}
	if err := a.saveTasksLocked(a.tasks); err != nil {
		*t = previous
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *app) deleteTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	if a.tasks == nil {
		a.tasks = a.loadTasksLocked()
	}
	kept := make([]*scheduledTask, 0, len(a.tasks))
	found := false
	for _, t := range a.tasks {
		if t.ID == body.ID {
			if t.Running {
				writeError(w, http.StatusConflict, "task is already running")
				return
			}
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such task")
		return
	}
	if err := a.saveTasksLocked(kept); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.tasks = kept
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": body.ID})
}

func (a *app) runTaskNow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	a.tasksMu.Lock()
	if a.tasks == nil {
		a.tasks = a.loadTasksLocked()
	}
	t := a.findTaskLocked(body.ID)
	if t == nil {
		a.tasksMu.Unlock()
		writeError(w, http.StatusNotFound, "no such task")
		return
	}
	if t.Running {
		a.tasksMu.Unlock()
		writeError(w, http.StatusConflict, "task is already running")
		return
	}
	t.Running = true
	if err := a.saveTasksLocked(a.tasks); err != nil {
		t.Running = false
		a.tasksMu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.tasksMu.Unlock()
	go a.runTask(t)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": t.ID, "note": "task started; poll GET /api/tasks for the result"})
}
