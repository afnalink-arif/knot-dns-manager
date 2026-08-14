package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// dnsdist belongs here even though it is the one service whose failure the
// dashboard cannot see: it fronts port 53, so when it loses its kresd backend
// the resolver goes dark while every health check stays green (238, 12 Aug
// 2026 — 11h outage fixable only by SSH, which was firewalled off). Restarting
// it is the documented fix for the stale-backend-IP trap, so it must be
// reachable from the UI.
var managedServices = []string{
	"dnsdist", "kresd", "dnstap-ingester", "prometheus", "node-exporter",
	"clickhouse", "redis", "postgres", "frontend", "caddy", "backend",
}

type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Health string `json:"health,omitempty"`
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	// Use docker ps directly — works reliably from inside any container.
	// Detect project name from our own container's labels.
	projectName := detectProjectName()

	services := []ServiceStatus{}
	for _, svc := range managedServices {
		st := ServiceStatus{Name: svc, Status: "stopped"}

		filter := fmt.Sprintf("label=com.docker.compose.service=%s", svc)
		var args []string
		if projectName != "" {
			projectFilter := fmt.Sprintf("label=com.docker.compose.project=%s", projectName)
			args = []string{"ps", "-a", "--filter", filter, "--filter", projectFilter, "--format", "{{.State}}|{{.Status}}"}
		} else {
			args = []string{"ps", "-a", "--filter", filter, "--format", "{{.State}}|{{.Status}}"}
		}

		out, err := exec.CommandContext(r.Context(), "docker", args...).Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			line := strings.TrimSpace(string(out))
			parts := strings.SplitN(line, "|", 2)
			st.Status = strings.ToLower(parts[0])
			if len(parts) > 1 {
				statusDetail := strings.ToLower(parts[1])
				if strings.Contains(statusDetail, "(healthy)") {
					st.Health = "healthy"
				} else if strings.Contains(statusDetail, "(unhealthy)") {
					st.Health = "unhealthy"
				}
			}
		}

		services = append(services, st)
	}

	writeJSON(w, services)
}

// detectProjectName reads the compose project name from this container's labels.
func detectProjectName() string {
	hostname, err := exec.Command("hostname").Output()
	if err != nil {
		return ""
	}
	out, err := exec.Command(
		"docker", "inspect", strings.TrimSpace(string(hostname)),
		"--format", `{{index .Config.Labels "com.docker.compose.project"}}`,
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Server) handleRestartService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Service == "" {
		http.Error(w, `{"error":"service name required"}`, http.StatusBadRequest)
		return
	}

	valid := false
	for _, svc := range managedServices {
		if svc == req.Service {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, `{"error":"invalid service name"}`, http.StatusBadRequest)
		return
	}

	// Find the container name for this service
	containerName := findContainerName(req.Service)
	if containerName == "" {
		http.Error(w, `{"error":"container not found"}`, http.StatusNotFound)
		return
	}

	// For backend, restart in background since it kills itself
	if req.Service == "backend" {
		go func() {
			exec.Command("docker", "restart", containerName).Run()
		}()
		writeJSON(w, map[string]string{"message": "backend restarting"})
		return
	}

	out, err := exec.CommandContext(r.Context(), "docker", "restart", containerName).CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, strings.TrimSpace(string(out))), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"message": req.Service + " restarted"})
}

func (s *Server) handleRestartAll(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	groups := []struct {
		label    string
		services []string
	}{
		{"Infrastructure", []string{"clickhouse", "redis", "postgres"}},
		// dnsdist last in this group, and strictly after kresd: it resolves the
		// kresd backend IP once at startup, so restarting it earlier would just
		// re-pin the old address. Mirrors update.sh step 4.
		{"DNS pipeline", []string{"dnstap-ingester", "kresd", "dnsdist"}},
		{"Monitoring", []string{"prometheus", "node-exporter"}},
		{"Frontend", []string{"frontend", "caddy"}},
	}

	for _, g := range groups {
		fmt.Fprintf(w, "data: Restarting %s...\n\n", g.label)
		flusher.Flush()

		for _, svc := range g.services {
			containerName := findContainerName(svc)
			if containerName == "" {
				fmt.Fprintf(w, "data: [ERROR] %s: container not found\n\n", svc)
				flusher.Flush()
				continue
			}
			out, err := exec.CommandContext(r.Context(), "docker", "restart", containerName).CombinedOutput()
			if err != nil {
				fmt.Fprintf(w, "data: [ERROR] %s: %s\n\n", svc, strings.TrimSpace(string(out)))
			} else {
				fmt.Fprintf(w, "data: [OK] %s restarted\n\n", svc)
			}
			flusher.Flush()
		}
	}

	// Restart backend last
	fmt.Fprintf(w, "data: Restarting backend...\n\n")
	flusher.Flush()

	go func() {
		if name := findContainerName("backend"); name != "" {
			exec.Command("docker", "restart", name).Run()
		}
	}()

	fmt.Fprintf(w, "event: done\ndata: All services restarted\n\n")
	flusher.Flush()
}

// handleServiceLogs returns the tail of a service's container logs as plain
// text. This is the diagnostic that every incident this week needed and SSH
// was the only way to get: MDB_MAP_FULL only shows in kresd's policy-loader
// output, dnsdist's stale-backend marking only in dnsdist's log. Read-only,
// name-allowlisted, tail-capped — safe to expose to the cluster controller.
func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	service := chi.URLParam(r, "name")
	valid := false
	for _, svc := range managedServices {
		if svc == service {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, `{"error":"invalid service name"}`, http.StatusBadRequest)
		return
	}

	tail := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		if v, err := strconv.Atoi(t); err == nil && v > 0 {
			tail = v
		}
	}
	if tail > 1000 {
		tail = 1000
	}

	containerName := findContainerName(service)
	if containerName == "" {
		http.Error(w, `{"error":"container not found"}`, http.StatusNotFound)
		return
	}

	// docker logs writes to both streams; CombinedOutput captures the lot.
	out, err := exec.CommandContext(r.Context(), "docker", "logs", "--tail",
		strconv.Itoa(tail), "--timestamps", containerName).CombinedOutput()
	if err != nil && len(out) == 0 {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, strings.TrimSpace(err.Error())), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(out)
}

// findContainerName finds the docker container name for a compose service.
func findContainerName(service string) string {
	projectName := detectProjectName()
	filter := fmt.Sprintf("label=com.docker.compose.service=%s", service)
	var args []string
	if projectName != "" {
		projectFilter := fmt.Sprintf("label=com.docker.compose.project=%s", projectName)
		args = []string{"ps", "-a", "--filter", filter, "--filter", projectFilter, "--format", "{{.Names}}"}
	} else {
		args = []string{"ps", "-a", "--filter", filter, "--format", "{{.Names}}"}
	}
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
