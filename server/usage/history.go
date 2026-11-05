package usage

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

const (
	ScopeGlobal = "global"
	ScopeUser   = "user"
	ScopeClient = "client"
	ScopeTunnel = "tunnel"
	ScopeHost   = "host"
)

type Point struct {
	Time     int64 `json:"t"`
	Upload   int64 `json:"u"`
	Download int64 `json:"d"`
}

type Resource struct {
	Key        string  `json:"key"`
	Scope      string  `json:"scope"`
	ID         int     `json:"id,omitempty"`
	UserID     int     `json:"user_id,omitempty"`
	ClientID   int     `json:"client_id,omitempty"`
	Name       string  `json:"name,omitempty"`
	Mode       string  `json:"mode,omitempty"`
	LimitBytes int64   `json:"limit_bytes,omitempty"`
	TotalBytes int64   `json:"total_bytes"`
	Points1m   []Point `json:"points_1m,omitempty"`
	Points5m   []Point `json:"points_5m,omitempty"`
}

type SnapshotFile struct {
	Version   int                  `json:"version"`
	UpdatedAt int64                `json:"updated_at"`
	Resources map[string]*Resource `json:"resources"`
}

type cumulative struct {
	in, out int64
	at      time.Time
}

type History struct {
	mu sync.RWMutex

	resources map[string]*Resource
	previous  map[string]cumulative
	updatedAt time.Time
	dirty     bool
	path      string
	options   Options
	collect   func() totalsSnapshot
}

type Options struct {
	Path           string
	SampleInterval time.Duration
	Retention1m    time.Duration
	Retention5m    time.Duration
	SaveEvery      int
	MaxResources   int
}

var (
	defaultHistory *History
	startOnce      sync.Once
	stopOnce       sync.Once
	stopCh         = make(chan struct{})
)

// Start begins asynchronous minute sampling. It is safe to call repeatedly;
// only one sampler is created per process.
func Start() {
	if !beego.AppConfig.DefaultBool("usage_history_enabled", true) {
		return
	}
	startOnce.Do(func() {
		path := strings.TrimSpace(beego.AppConfig.String("usage_history_path"))
		if path == "" {
			path = filepath.Join("conf", "usage_history.json")
		}
		options := Options{
			Path:           common.ResolveConfigPath(path),
			SampleInterval: time.Duration(configInt("usage_history_interval", 60)) * time.Second,
			Retention1m:    time.Duration(configInt("usage_history_1m_hours", 48)) * time.Hour,
			Retention5m:    time.Duration(configInt("usage_history_5m_days", 90)) * 24 * time.Hour,
			SaveEvery:      configInt("usage_history_save_every", 5),
			MaxResources:   configInt("usage_history_max_resources", 20000),
		}
		if options.SampleInterval < 10*time.Second {
			options.SampleInterval = time.Minute
		}
		defaultHistory = New(options)
		if err := defaultHistory.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
			logs.Warn("读取历史用量数据失败：%v", err)
		}
		defaultHistory.Sample(time.Now())
		go defaultHistory.run()
	})
}

func Default() *History {
	return defaultHistory
}

// Stop flushes the current snapshot and stops the background sampler.
func Stop() {
	stopOnce.Do(func() {
		close(stopCh)
		if defaultHistory != nil {
			defaultHistory.Sample(time.Now())
			if err := defaultHistory.Save(); err != nil {
				logs.Warn("保存历史用量数据失败：%v", err)
			}
		}
	})
}

func New(options Options) *History {
	if options.SampleInterval <= 0 {
		options.SampleInterval = time.Minute
	}
	if options.Retention1m <= 0 {
		options.Retention1m = 48 * time.Hour
	}
	if options.Retention5m <= 0 {
		options.Retention5m = 90 * 24 * time.Hour
	}
	if options.SaveEvery <= 0 {
		options.SaveEvery = 5
	}
	if options.MaxResources <= 0 {
		options.MaxResources = 20000
	}
	history := &History{
		resources: make(map[string]*Resource),
		previous:  make(map[string]cumulative),
		path:      options.Path,
		options:   options,
		collect:   collectTotals,
	}
	return history
}

func (h *History) run() {
	ticker := time.NewTicker(h.options.SampleInterval)
	defer ticker.Stop()
	saveCounter := 0
	for {
		select {
		case now := <-ticker.C:
			h.Sample(now)
			saveCounter++
			if saveCounter < h.options.SaveEvery {
				continue
			}
			saveCounter = 0
			if err := h.Save(); err != nil {
				logs.Warn("保存历史用量数据失败：%v", err)
			}
		case <-stopCh:
			return
		}
	}
}

func (h *History) Load() error {
	if h == nil || strings.TrimSpace(h.path) == "" {
		return nil
	}
	raw, err := os.ReadFile(h.path)
	if err != nil {
		return err
	}
	var snapshot SnapshotFile
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("parse usage history: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resources = snapshot.Resources
	if h.resources == nil {
		h.resources = make(map[string]*Resource)
	}
	h.updatedAt = time.Unix(snapshot.UpdatedAt, 0)
	for _, resource := range h.resources {
		resource.Points1m = trimBefore(resource.Points1m, time.Now().Add(-h.options.Retention1m).Unix())
		resource.Points5m = trimBefore(resource.Points5m, time.Now().Add(-h.options.Retention5m).Unix())
	}
	h.trimResourcesLocked()
	return nil
}

func (h *History) Save() error {
	if h == nil || strings.TrimSpace(h.path) == "" {
		return nil
	}
	h.mu.RLock()
	snapshot := SnapshotFile{
		Version:   1,
		UpdatedAt: h.updatedAt.Unix(),
		Resources: h.resources,
	}
	raw, err := json.Marshal(snapshot)
	h.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	temp := h.path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, h.path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	h.mu.Lock()
	h.dirty = false
	h.mu.Unlock()
	return nil
}

func (h *History) Sample(now time.Time) {
	if h == nil {
		return
	}
	collect := h.collect
	if collect == nil {
		collect = collectTotals
	}
	current := collect()
	h.mu.Lock()
	defer h.mu.Unlock()
	minuteTime := now.Truncate(time.Minute).Unix()
	fiveMinuteTime := now.Truncate(5 * time.Minute).Unix()
	for key, total := range current.resources {
		resource := h.resources[key]
		if resource == nil {
			resource = &Resource{Key: key}
			h.resources[key] = resource
		}
		resource.Scope = total.Scope
		resource.ID = total.ID
		resource.UserID = total.UserID
		resource.ClientID = total.ClientID
		resource.Name = total.Name
		resource.Mode = total.Mode
		resource.LimitBytes = total.LimitBytes
		resource.TotalBytes = total.In + total.Out
		previous, seen := h.previous[key]
		var deltaIn, deltaOut int64
		if seen && total.In >= previous.in && total.Out >= previous.out {
			deltaIn = total.In - previous.in
			deltaOut = total.Out - previous.out
		}
		h.previous[key] = cumulative{in: total.In, out: total.Out, at: now}
		if !seen {
			continue
		}
		appendPoint(resource, &resource.Points1m, Point{Time: minuteTime, Upload: deltaIn, Download: deltaOut})
		appendPoint(resource, &resource.Points5m, Point{Time: fiveMinuteTime, Upload: deltaIn, Download: deltaOut})
	}
	h.updatedAt = now
	h.dirty = true
	cutoff1m := now.Add(-h.options.Retention1m).Unix()
	cutoff5m := now.Add(-h.options.Retention5m).Unix()
	for _, resource := range h.resources {
		resource.Points1m = trimBefore(resource.Points1m, cutoff1m)
		resource.Points5m = trimBefore(resource.Points5m, cutoff5m)
	}
	h.trimResourcesLocked()
}

func appendPoint(resource *Resource, points *[]Point, point Point) {
	if n := len(*points); n > 0 && (*points)[n-1].Time == point.Time {
		(*points)[n-1].Upload += point.Upload
		(*points)[n-1].Download += point.Download
		return
	}
	*points = append(*points, point)
}

func trimBefore(points []Point, cutoff int64) []Point {
	if len(points) == 0 {
		return points
	}
	index := 0
	for index < len(points) && points[index].Time < cutoff {
		index++
	}
	if index == 0 {
		return points
	}
	return append([]Point(nil), points[index:]...)
}

func (h *History) trimResourcesLocked() {
	if h == nil || h.options.MaxResources <= 0 || len(h.resources) <= h.options.MaxResources {
		return
	}
	type candidate struct {
		key  string
		last int64
	}
	candidates := make([]candidate, 0, len(h.resources))
	for key, resource := range h.resources {
		if key == "global:0" || resource == nil {
			continue
		}
		last := int64(0)
		if previous, ok := h.previous[key]; ok && previous.at.Unix() > last {
			last = previous.at.Unix()
		}
		if n := len(resource.Points1m); n > 0 && resource.Points1m[n-1].Time > last {
			last = resource.Points1m[n-1].Time
		}
		if n := len(resource.Points5m); n > 0 && resource.Points5m[n-1].Time > last {
			last = resource.Points5m[n-1].Time
		}
		candidates = append(candidates, candidate{key: key, last: last})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].last != candidates[j].last {
			return candidates[i].last < candidates[j].last
		}
		return candidates[i].key < candidates[j].key
	})
	remove := len(h.resources) - h.options.MaxResources
	for _, item := range candidates {
		if remove <= 0 {
			break
		}
		delete(h.resources, item.key)
		delete(h.previous, item.key)
		remove--
	}
}

type totalResource struct {
	Resource
	In, Out int64
}

type totalsSnapshot struct {
	resources map[string]*totalResource
}

func collectTotals() totalsSnapshot {
	snapshot := totalsSnapshot{resources: make(map[string]*totalResource)}
	var globalIn, globalOut int64
	file.GetDb().JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		id, userID, name, flow, limit := client.Id, client.UserId, client.Remark, client.Flow, int64(0)
		client.RUnlock()
		if flow == nil {
			return true
		}
		in, out, flowLimit := flow.Snapshot()
		globalIn += in
		globalOut += out
		if flowLimit > 0 {
			limit = flowLimit << 20
		}
		snapshot.resources[fmt.Sprintf("client:%d", id)] = &totalResource{
			Resource: Resource{Key: fmt.Sprintf("client:%d", id), Scope: ScopeClient, ID: id, UserID: userID, Name: name, LimitBytes: limit},
			In:       in, Out: out,
		}
		if userID > 0 {
			key := fmt.Sprintf("user:%d", userID)
			resource := snapshot.resources[key]
			if resource == nil {
				resource = &totalResource{Resource: Resource{Key: key, Scope: ScopeUser, ID: userID}}
				snapshot.resources[key] = resource
			}
			resource.In += in
			resource.Out += out
			if resource.Name == "" {
				if user, err := file.GetDb().GetUser(userID); err == nil && user != nil {
					user.RLock()
					resource.Name = user.UserName
					user.RUnlock()
				}
			}
		}
		return true
	})
	snapshot.resources["global:0"] = &totalResource{
		Resource: Resource{Key: "global:0", Scope: ScopeGlobal, Name: "全部流量"},
		In:       globalIn, Out: globalOut,
	}
	file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*file.Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		id, mode, remark, flow, client := tunnel.Id, strings.ToLower(tunnel.Mode), tunnel.Remark, tunnel.Flow, tunnel.Client
		tunnel.RUnlock()
		if flow == nil || strings.Contains(mode, "p2p") {
			return true
		}
		in, out, limit := flow.Snapshot()
		clientID, userID := 0, 0
		if client != nil {
			client.RLock()
			clientID, userID = client.Id, client.UserId
			client.RUnlock()
		}
		key := fmt.Sprintf("tunnel:%d", id)
		snapshot.resources[key] = &totalResource{
			Resource: Resource{Key: key, Scope: ScopeTunnel, ID: id, UserID: userID, ClientID: clientID, Name: remark, Mode: mode, LimitBytes: limit << 20},
			In:       in, Out: out,
		}
		return true
	})
	file.GetDb().JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		id, hostName, flow, client := host.Id, host.Host, host.Flow, host.Client
		host.RUnlock()
		if flow == nil {
			return true
		}
		in, out, limit := flow.Snapshot()
		clientID, userID := 0, 0
		if client != nil {
			client.RLock()
			clientID, userID = client.Id, client.UserId
			client.RUnlock()
		}
		key := fmt.Sprintf("host:%d", id)
		snapshot.resources[key] = &totalResource{
			Resource: Resource{Key: key, Scope: ScopeHost, ID: id, UserID: userID, ClientID: clientID, Name: hostName, Mode: host.Scheme, LimitBytes: limit << 20},
			In:       in, Out: out,
		}
		return true
	})
	return snapshot
}

type Query struct {
	Scope  string
	ID     int
	UserID int
	Client int
	From   time.Time
	To     time.Time
}

type Summary struct {
	Resource
	UploadBytes     int64   `json:"upload_bytes"`
	DownloadBytes   int64   `json:"download_bytes"`
	UploadBytes5m   int64   `json:"upload_bytes_5m"`
	DownloadBytes5m int64   `json:"download_bytes_5m"`
	PeakUploadBPS   int64   `json:"peak_upload_bps"`
	PeakDownloadBPS int64   `json:"peak_download_bps"`
	P95UploadBPS    int64   `json:"p95_upload_bps"`
	P95DownloadBPS  int64   `json:"p95_download_bps"`
	QuotaPercent    float64 `json:"quota_percent,omitempty"`
	EstimatedDays   float64 `json:"estimated_days,omitempty"`
}

func (h *History) Query(query Query) []Summary {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]Summary, 0)
	for _, resource := range h.resources {
		if !matchesQuery(resource, query) {
			continue
		}
		summary := summarize(resource, query, h.options.SampleInterval)
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].UploadBytes+result[i].DownloadBytes > result[j].UploadBytes+result[j].DownloadBytes
	})
	return result
}

func matchesQuery(resource *Resource, query Query) bool {
	if resource == nil {
		return false
	}
	if query.Scope != "" && resource.Scope != query.Scope {
		return false
	}
	if query.ID != 0 && resource.ID != query.ID {
		return false
	}
	if query.UserID != 0 && resource.UserID != query.UserID {
		return false
	}
	if query.Client != 0 && resource.ClientID != query.Client {
		return false
	}
	return true
}

func summarize(resource *Resource, query Query, sampleInterval time.Duration) Summary {
	summary := Summary{Resource: *resource}
	summary.Points1m = nil
	summary.Points5m = nil
	points := filterPoints(resource.Points1m, query.From, query.To)
	points5m := filterPoints(resource.Points5m, query.From, query.To)
	uploadRates := make([]int64, 0, len(points))
	downloadRates := make([]int64, 0, len(points))
	sampleSeconds := int64(sampleInterval / time.Second)
	if sampleSeconds <= 0 {
		sampleSeconds = 60
	}
	for _, point := range points {
		summary.UploadBytes += point.Upload
		summary.DownloadBytes += point.Download
		uploadRates = append(uploadRates, point.Upload/sampleSeconds)
		downloadRates = append(downloadRates, point.Download/sampleSeconds)
	}
	for _, point := range points5m {
		summary.UploadBytes5m += point.Upload
		summary.DownloadBytes5m += point.Download
	}
	sort.Slice(uploadRates, func(i, j int) bool { return uploadRates[i] < uploadRates[j] })
	sort.Slice(downloadRates, func(i, j int) bool { return downloadRates[i] < downloadRates[j] })
	summary.PeakUploadBPS = maxInt64(uploadRates)
	summary.PeakDownloadBPS = maxInt64(downloadRates)
	summary.P95UploadBPS = percentile(uploadRates, 95)
	summary.P95DownloadBPS = percentile(downloadRates, 95)
	if summary.LimitBytes > 0 {
		summary.QuotaPercent = float64(summary.TotalBytes) / float64(summary.LimitBytes) * 100
	}
	if len(points) > 0 {
		avg := float64(summary.UploadBytes+summary.DownloadBytes) / float64(len(points))
		dailyBytes := avg * (float64(24*time.Hour) / float64(sampleInterval))
		if dailyBytes > 0 && summary.LimitBytes > summary.TotalBytes {
			summary.EstimatedDays = float64(summary.LimitBytes-summary.TotalBytes) / dailyBytes
		}
	}
	return summary
}

// Series returns permission-filtered raw points for charts. Only the selected
// resolution is populated; the other slice is cleared to keep API payloads
// bounded.
func (h *History) Series(query Query, bucket string) ([]Resource, int64) {
	if h == nil {
		return nil, 0
	}
	if bucket != "5m" {
		bucket = "1m"
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]Resource, 0)
	for _, resource := range h.resources {
		if !matchesQuery(resource, query) {
			continue
		}
		copyResource := *resource
		copyResource.Points1m = nil
		copyResource.Points5m = nil
		if bucket == "5m" {
			copyResource.Points5m = append([]Point(nil), filterPoints(resource.Points5m, query.From, query.To)...)
		} else {
			copyResource.Points1m = append([]Point(nil), filterPoints(resource.Points1m, query.From, query.To)...)
		}
		result = append(result, copyResource)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Scope != result[j].Scope {
			return result[i].Scope < result[j].Scope
		}
		return result[i].Key < result[j].Key
	})
	interval := int64(h.options.SampleInterval / time.Second)
	if bucket == "5m" {
		interval = int64((5 * time.Minute) / time.Second)
	}
	if interval <= 0 {
		interval = 60
	}
	return result, interval
}

func filterPoints(points []Point, from, to time.Time) []Point {
	if from.IsZero() && to.IsZero() {
		return points
	}
	result := make([]Point, 0, len(points))
	for _, point := range points {
		at := time.Unix(point.Time, 0)
		if !from.IsZero() && at.Before(from) {
			continue
		}
		if !to.IsZero() && at.After(to) {
			continue
		}
		result = append(result, point)
	}
	return result
}

func percentile(sorted []int64, percent int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted) - 1) * percent / 100
	return sorted[index]
}

func maxInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

func (h *History) ExportCSV(writer io.Writer, query Query, bucket string) error {
	if h == nil {
		return errors.New("history is not initialized")
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write([]string{"time", "key", "scope", "id", "user_id", "client_id", "name", "upload_bytes", "download_bytes"}); err != nil {
		return err
	}
	for _, resource := range h.resources {
		if !matchesQuery(resource, query) {
			continue
		}
		points := resource.Points1m
		if bucket == "5m" {
			points = resource.Points5m
		}
		for _, point := range filterPoints(points, query.From, query.To) {
			row := []string{
				time.Unix(point.Time, 0).Format(time.RFC3339),
				resource.Key,
				resource.Scope,
				strconv.Itoa(resource.ID),
				strconv.Itoa(resource.UserID),
				strconv.Itoa(resource.ClientID),
				resource.Name,
				strconv.FormatInt(point.Upload, 10),
				strconv.FormatInt(point.Download, 10),
			}
			if err := csvWriter.Write(row); err != nil {
				return err
			}
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func configInt(key string, fallback int) int {
	if raw := strings.TrimSpace(beego.AppConfig.String(key)); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			return value
		}
	}
	return fallback
}
