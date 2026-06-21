package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

type config struct {
	intervalSec   int
	memAlertPct   float64
	cpuAlertPct   float64
	topN          int
	cooldownMin   int
	logDir        string
	retentionDays int
	tgBotToken    string
	tgChatID      string
}

type procSnapshot struct {
	pid        int32
	name       string
	ppid       int32
	parentName string
	user       string
	rss        uint64
	cpuUser    float64
	cpuSys     float64
	cmdline    string
}

type procResult struct {
	snap   procSnapshot
	cpuPct float64
}

func loadConfig() config {
	_ = godotenv.Load("../.env")
	_ = godotenv.Load(".env")

	getInt := func(key string, def int) int {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}
	getFloat := func(key string, def float64) float64 {
		if v := os.Getenv(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		}
		return def
	}
	getStr := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	return config{
		intervalSec:   getInt("INTERVAL_SECONDS", 60),
		memAlertPct:   getFloat("MEM_ALERT_PCT", 85),
		cpuAlertPct:   getFloat("CPU_ALERT_PCT", 90),
		topN:          getInt("TOP_N", 10),
		cooldownMin:   getInt("ALERT_COOLDOWN_MIN", 15),
		logDir:        getStr("LOG_DIR", "./logs"),
		retentionDays: getInt("LOG_RETENTION_DAYS", 2),
		tgBotToken:    os.Getenv("DEVELOPER_TELEGRAM_BOT_TOKEN"),
		tgChatID:      os.Getenv("DEVELOPER_TELEGRAM_CHAT_ID"),
	}
}

// openDailyLog opens (or reopens on date rollover) the daily log file and
// redirects the log package output to it. Returns the new file and today's date string.
func openDailyLog(logDir, currentDate string) (*os.File, string) {
	today := time.Now().Format("2006-01-02")
	if today == currentDate {
		return nil, currentDate
	}
	path := filepath.Join(logDir, "monitor-"+today+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open log file %s: %v\n", path, err)
		return nil, currentDate
	}
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime)
	return f, today
}

func snapshotProcs() map[int32]procSnapshot {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	snaps := make(map[int32]procSnapshot, len(procs))
	for _, p := range procs {
		name, _ := p.Name()
		ppid, _ := p.Ppid()
		user, _ := p.Username()
		rss := uint64(0)
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = mi.RSS
		}
		cmdline, _ := p.Cmdline()
		times, _ := p.Times()
		cpu, sys := 0.0, 0.0
		if times != nil {
			cpu = times.User
			sys = times.System
		}
		snaps[p.Pid] = procSnapshot{
			pid:     p.Pid,
			name:    name,
			ppid:    ppid,
			user:    user,
			rss:     rss,
			cpuUser: cpu,
			cpuSys:  sys,
			cmdline: cmdline,
		}
	}
	for pid, snap := range snaps {
		if parent, ok := snaps[snap.ppid]; ok {
			snap.parentName = parent.name
		}
		snaps[pid] = snap
	}
	return snaps
}

func diffProcs(before, after map[int32]procSnapshot, wallSec float64) []procResult {
	numCPU := float64(runtime.NumCPU())
	results := make([]procResult, 0, len(after))
	for pid, a := range after {
		b, ok := before[pid]
		if !ok {
			b = a
		}
		deltaCPU := (a.cpuUser - b.cpuUser) + (a.cpuSys - b.cpuSys)
		cpuPct := 0.0
		if wallSec > 0 {
			cpuPct = (deltaCPU / wallSec / numCPU) * 100
			if cpuPct < 0 {
				cpuPct = 0
			}
		}
		results = append(results, procResult{snap: a, cpuPct: cpuPct})
	}
	return results
}

func topByCPU(procs []procResult, n int) []procResult {
	sorted := make([]procResult, len(procs))
	copy(sorted, procs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].cpuPct > sorted[j].cpuPct })
	if n < len(sorted) {
		return sorted[:n]
	}
	return sorted
}

func topByMem(procs []procResult, n int) []procResult {
	sorted := make([]procResult, len(procs))
	copy(sorted, procs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].snap.rss > sorted[j].snap.rss })
	if n < len(sorted) {
		return sorted[:n]
	}
	return sorted
}

func fmtMB(bytes uint64) string {
	return fmt.Sprintf("%.0fMB", float64(bytes)/1024/1024)
}

func formatTable(procs []procResult, title string, detailed bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== %s ===\n", title))
	sb.WriteString(fmt.Sprintf("%-6s %-6s %-16s %-16s %-8s %8s %7s\n",
		"PID", "PPID", "NAME", "PARENT", "USER", "MEM", "CPU%"))
	sb.WriteString(strings.Repeat("-", 80) + "\n")
	for _, r := range procs {
		s := r.snap
		sb.WriteString(fmt.Sprintf("%-6d %-6d %-16s %-16s %-8s %8s %6.1f%%\n",
			s.pid, s.ppid,
			truncate(s.name, 16), truncate(s.parentName, 16),
			truncate(s.user, 8), fmtMB(s.rss), r.cpuPct))
		if detailed && s.cmdline != "" {
			sb.WriteString(fmt.Sprintf("       cmd: %s\n", truncate(s.cmdline, 120)))
		}
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func compactLine(vm *mem.VirtualMemoryStat, totalCPU float64, topMem []procResult) string {
	usedGB := float64(vm.Used) / 1024 / 1024 / 1024
	totalGB := float64(vm.Total) / 1024 / 1024 / 1024
	ts := time.Now().Format("15:04:05")
	top := ""
	if len(topMem) > 0 {
		r := topMem[0]
		top = fmt.Sprintf(" | top: %s %s(pid %d←%s)", r.snap.name, fmtMB(r.snap.rss), r.snap.pid, r.snap.parentName)
	}
	return fmt.Sprintf("%s RAM %.0f%% %.1f/%.1fG | CPU %.0f%%%s",
		ts, vm.UsedPercent, usedGB, totalGB, totalCPU, top)
}

func appendLog(path, content string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("log write error: %v", err)
		return
	}
	defer f.Close()
	f.WriteString(content)
}

func sendTelegram(token, chatID, text string) error {
	if token == "" || chatID == "" {
		return fmt.Errorf("telegram creds missing (DEVELOPER_TELEGRAM_BOT_TOKEN / DEVELOPER_TELEGRAM_CHAT_ID)")
	}
	if len(text) > 4096 {
		text = text[:4090] + "\n..."
	}
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram HTTP %d", resp.StatusCode)
	}
	return nil
}

func pruneOldLogs(logDir string, retentionDays int) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(logDir, e.Name())
			_ = os.Remove(path)
			log.Printf("pruned old log: %s", path)
		}
	}
}

func main() {
	cfg := loadConfig()

	if err := os.MkdirAll(cfg.logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create log dir %s: %v\n", cfg.logDir, err)
		os.Exit(1)
	}

	// Open today's log file and redirect log package to it.
	// PM2 only captures fmt.Println (stdout) — the compact heartbeat line.
	var logFile *os.File
	var currentDate string
	logFile, currentDate = openDailyLog(cfg.logDir, "")
	if logFile != nil {
		defer logFile.Close()
	}

	log.Printf("monitor started | interval=%ds mem_alert=%.0f%% cpu_alert=%.0f%% top=%d cooldown=%dmin retention=%dd",
		cfg.intervalSec, cfg.memAlertPct, cfg.cpuAlertPct, cfg.topN, cfg.cooldownMin, cfg.retentionDays)

	pruneOldLogs(cfg.logDir, cfg.retentionDays)
	lastPrune := time.Now()
	var lastAlert time.Time

	for {
		tickStart := time.Now()

		// Reopen log file on date rollover
		if newFile, newDate := openDailyLog(cfg.logDir, currentDate); newFile != nil {
			if logFile != nil {
				logFile.Close()
			}
			logFile = newFile
			currentDate = newDate
		}

		// --- 1. Snapshot procs (two-sample for CPU delta) ---
		before := snapshotProcs()
		time.Sleep(1 * time.Second)
		after := snapshotProcs()
		wallSec := time.Since(tickStart).Seconds()

		procs := diffProcs(before, after, wallSec)

		// --- 2. System memory ---
		vm, err := mem.VirtualMemory()
		if err != nil {
			log.Printf("mem error: %v", err)
			time.Sleep(time.Duration(cfg.intervalSec) * time.Second)
			continue
		}
		swap, _ := mem.SwapMemory()

		// --- 3. Total CPU (sum of per-process pcts, each already normalised by numCPU in diffProcs) ---
		var totalCPU float64
		for _, r := range procs {
			totalCPU += r.cpuPct
		}
		if totalCPU > 100 {
			totalCPU = 100
		}

		byMem := topByMem(procs, cfg.topN)
		byCPU := topByCPU(procs, cfg.topN)

		// --- 4. Compact heartbeat → stdout (PM2 captures this, tiny) ---
		fmt.Println(compactLine(vm, totalCPU, byMem))

		// --- 5. Full tables → daily log file (managed by us, not PM2) ---
		logPath := filepath.Join(cfg.logDir, "monitor-"+time.Now().Format("2006-01-02")+".log")
		swapLine := ""
		if swap != nil {
			swapLine = fmt.Sprintf("SWAP: %.0f%% %.0fMB/%.0fMB | ",
				swap.UsedPercent, float64(swap.Used)/1024/1024, float64(swap.Total)/1024/1024)
		}
		header := fmt.Sprintf("\n[%s] RAM=%.1f%% CPU=%.1f%% | %s\n",
			time.Now().Format("2006-01-02 15:04:05"), vm.UsedPercent, totalCPU, swapLine)
		appendLog(logPath, header+formatTable(byCPU, "TOP CPU", false)+"\n"+formatTable(byMem, "TOP MEM", false)+"\n")

		// --- 6. Alert ---
		memBreached := vm.UsedPercent >= cfg.memAlertPct
		cpuBreached := totalCPU >= cfg.cpuAlertPct
		cooldownPassed := time.Since(lastAlert) >= time.Duration(cfg.cooldownMin)*time.Minute

		if (memBreached || cpuBreached) && cooldownPassed {
			lastAlert = time.Now()

			reason := ""
			if memBreached {
				reason += fmt.Sprintf("RAM %.0f%%", vm.UsedPercent)
			}
			if cpuBreached {
				if reason != "" {
					reason += " + "
				}
				reason += fmt.Sprintf("CPU %.0f%%", totalCPU)
			}

			alertPath := filepath.Join(cfg.logDir, fmt.Sprintf("alert-%d.log", time.Now().Unix()))
			alertContent := fmt.Sprintf("=== ALERT %s [%s] ===\n", reason, time.Now().Format("2006-01-02 15:04:05"))
			alertContent += fmt.Sprintf("RAM: %.1f%% used (%.0fMB / %.0fMB free)\n",
				vm.UsedPercent, float64(vm.Used)/1024/1024, float64(vm.Available)/1024/1024)
			if swap != nil {
				alertContent += fmt.Sprintf("SWAP: %.1f%% used (%.0fMB / %.0fMB)\n",
					swap.UsedPercent, float64(swap.Used)/1024/1024, float64(swap.Total)/1024/1024)
			}
			alertContent += fmt.Sprintf("CPUs: %d | Total load: %.1f%%\n\n", runtime.NumCPU(), totalCPU)
			alertContent += formatTable(byCPU, "TOP CPU", true) + "\n"
			alertContent += formatTable(byMem, "TOP MEM", true) + "\n"
			appendLog(alertPath, alertContent)
			log.Printf("alert written: %s", alertPath)

			top5 := byMem
			if len(top5) > 5 {
				top5 = top5[:5]
			}
			var tgLines []string
			tgLines = append(tgLines, fmt.Sprintf("🚨 <b>MONITOR ALERT: %s</b>", reason))
			tgLines = append(tgLines, fmt.Sprintf("RAM: <b>%.1f%%</b> | CPU: <b>%.1f%%</b>", vm.UsedPercent, totalCPU))
			tgLines = append(tgLines, "")
			tgLines = append(tgLines, "<b>Top memory consumers:</b>")
			for _, r := range top5 {
				s := r.snap
				tgLines = append(tgLines, fmt.Sprintf(
					"• <code>%s</code> (pid %d ← %s) | %s | CPU %.1f%%",
					html.EscapeString(s.name), s.pid, html.EscapeString(s.parentName), fmtMB(s.rss), r.cpuPct))
			}

			if err := sendTelegram(cfg.tgBotToken, cfg.tgChatID, strings.Join(tgLines, "\n")); err != nil {
				log.Printf("telegram: %v", err)
			} else {
				log.Printf("telegram alert sent: %s", reason)
			}
		}

		// --- 7. Daily prune ---
		if time.Since(lastPrune) >= 24*time.Hour {
			pruneOldLogs(cfg.logDir, cfg.retentionDays)
			lastPrune = time.Now()
		}

		elapsed := time.Since(tickStart)
		if remaining := time.Duration(cfg.intervalSec)*time.Second - elapsed; remaining > 0 {
			time.Sleep(remaining)
		}
	}
}
