package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"io/ioutil"
	"math"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f%s", float64(b)/float64(div), "KMGTPE"[exp:exp+1])
}

func formatUptime(seconds int64) string {
	if seconds >= 86400 {
		days := seconds / 86400
		return fmt.Sprintf("%d天", days)
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
}

func getCPUModel() string {
	out, err := exec.Command("sh", "-c", "lscpu | grep 'Model name'").Output()
	if err != nil {
		return runtime.GOARCH
	}
	parts := strings.SplitN(string(out), ":", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[1])
	}
	return runtime.GOARCH
}

func getVirtualizationType() string {
	out, err := exec.Command("systemd-detect-virt").Output()
	if err == nil {
		vtype := strings.TrimSpace(string(out))
		if vtype != "none" {
			return strings.ToUpper(vtype)
		}
	}
	return "Physical"
}

func getOSVersion() string {
	content, err := ioutil.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	lines := strings.Split(string(content), "\n")
	var id, version string
	for _, line := range lines {
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(line[3:], "\"")
		} else if strings.HasPrefix(line, "VERSION_ID=") {
			version = strings.Trim(line[11:], "\"")
		}
	}
	return fmt.Sprintf("%s-%s", id, version)
}

func getLoadAverage() map[string]float64 {
	avg, err := load.Avg()
	if err != nil {
		return map[string]float64{"error": -1}
	}
	return map[string]float64{
		"1min":  avg.Load1,
		"5min":  avg.Load5,
		"15min": avg.Load15,
	}
}

func getDiskUsage() map[string]interface{} {
	usage, err := disk.Usage("/")
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"total":   formatBytes(usage.Total),
		"used":    formatBytes(usage.Used),
		"percent": usage.UsedPercent,
	}
}

func getNetworkSpeed(interval time.Duration) (upload, download float64) {
	before, _ := net.IOCounters(false)
	time.Sleep(interval)
	after, _ := net.IOCounters(false)
	if len(before) > 0 && len(after) > 0 {
		upload = float64(after[0].BytesSent-before[0].BytesSent) / interval.Seconds()
		download = float64(after[0].BytesRecv-before[0].BytesRecv) / interval.Seconds()
	}
	return
}

func getAllDisksUsage() (map[string]interface{}, error) {
	partitions, err := disk.Partitions(true) // true获取所有，包括逻辑分区
	if err != nil {
		return nil, err
	}

	var total uint64 = 0
	var used uint64 = 0

	for _, p := range partitions {
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			// 有些盘可能无法访问，跳过
			continue
		}
		total += usage.Total
		used += usage.Used
	}

	var percent float64 = 0
	if total > 0 {
		percent = math.Round(float64(used)*10000/float64(total)) / 100 // 保留2位小数的百分比
	}

	diskInfo := map[string]interface{}{
		"total":   formatBytes(total),
		"used":    formatBytes(used),
		"percent": percent,
	}
	return diskInfo, nil
}

func getSystemInfo() map[string]interface{} {
	vmem, _ := mem.VirtualMemory()
	swap, _ := mem.SwapMemory()
	cpus, _ := cpu.Info()
	cpuPercent, _ := cpu.Percent(1*time.Second, false)
	hostInfo, _ := host.Info()
	netStats, _ := net.IOCounters(false)
	upload, download := getNetworkSpeed(1 * time.Second)
	uptimeSeconds, _ := host.Uptime()
	diskInfo, _ := getAllDisksUsage()

	return map[string]interface{}{
		"platform":         runtime.GOOS,
		"platform_version": hostInfo.PlatformVersion,
		"distribution":     getOSVersion(),
		"virtualization":   getVirtualizationType(),
		"architecture":     runtime.GOARCH,
		"cpu": map[string]interface{}{
			"model":   cpus[0].ModelName,
			"count":   runtime.NumCPU(),
			"percent": math.Round(cpuPercent[0]*100) / 100,
		},
		"memory": map[string]interface{}{
			"total":   formatBytes(vmem.Total),
			"used":    formatBytes(vmem.Used),
			"percent": math.Round(float64(vmem.Used)*10000/float64(vmem.Total)) / 100,
		},
		"swap": map[string]interface{}{
			"total":   formatBytes(swap.Total),
			"used":    formatBytes(swap.Used),
			"percent": math.Round(swap.UsedPercent*100) / 100,
		},
		"disk": diskInfo,
		"network": map[string]interface{}{
			"upload_speed":   formatBytes(uint64(upload)),
			"download_speed": formatBytes(uint64(download)),
			"upload_total":   formatBytes(netStats[0].BytesSent),
			"download_total": formatBytes(netStats[0].BytesRecv),
		},
		"load_average":  getLoadAverage(),
		"uptime":        formatUptime(int64(uptimeSeconds)),
		"boot_time":     time.Unix(int64(hostInfo.BootTime), 0).Format("2006-01-02 15:04:05"),
		"current_time":  time.Now().Format("2006-01-02 15:04:05"),
		"process_count": hostInfo.Procs,
	}
}

func reportToServer(data map[string]interface{}, url string) {
	body, _ := json.Marshal(data)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Println("Error reporting:", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		fmt.Println("Reported successfully.")
	} else {
		fmt.Println("Server returned status:", resp.StatusCode)
	}
}

func sendHeartbeat(url string) {
	heartbeat := map[string]interface{}{
		"status":    "online",
		"timestamp": time.Now().Unix(),
	}
	reportToServer(heartbeat, url)
}

func main() {
	info := getSystemInfo()

	// 将 info 转为 JSON 字符串
	jsonBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		fmt.Println("JSON encode error:", err)
	} else {
		fmt.Println(string(jsonBytes))
	}

	for {
		upload, download := getNetworkSpeed(1 * time.Second)

		fmt.Printf("Upload: %s , Download: %s\n", formatBytes(uint64(upload)), formatBytes(uint64(download)))

		//reportToServer(info, "http://your-java-server-url/report")
		//sendHeartbeat("http://your-java-server-url/heartbeat")
		time.Sleep(1 * time.Second)
	}
}
