package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type WebhookRequest struct {
	URL           string        `json:"url"`
	ID            string        `json:"id"`
	WebhookParams WebhookParams `json:"webHookParams,omitempty"`
}

type WebhookResponse struct {
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

type WebhookParams struct {
	ChannelID string `json:"channelId"`
}

type ReportPayload struct {
	ID            string        `json:"id"`
	URL           string        `json:"url"`
	WebhookParams WebhookParams `json:"webHookParams,omitempty"`
	Error         string        `json:"error,omitempty"`
}

func downloadVideo(url string, id string) (string, error) {
	outputTemplate := filepath.Join("downloads",
		fmt.Sprintf("%s_%%(id)s_%%(duration>%%H-%%M-%%S)s.%%(ext)s", id))

	cmd := exec.Command("yt-dlp", "--cookies",
		os.Getenv("COOKIES_FILE"), "-o", outputTemplate, url)
	outputBuffer := &bytes.Buffer{}
	cmd.Stdout = outputBuffer
	cmd.Stderr = os.Stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			return "", err
		}
	case <-time.After(10 * time.Minute):
		cmd.Process.Kill()
		return "", fmt.Errorf("download timed out")
	}
	log.Println("outputBuffer: ", outputBuffer.String())

	re := regexp.MustCompile(`(downloads/[\w_]*?_[\w-_]*?_\d\d-\d\d-\d\d\.(mp4|webm|mov|mkv))`)
	matches := re.FindStringSubmatch(outputBuffer.String())
	log.Println("matches: ", matches)
	if len(matches) > 0 {
		return matches[0], nil
	}

	return "", fmt.Errorf("failed to parse output file name from yt-dlp output")
}

func reportDownloadSuccess(id, file string, webHookParams WebhookParams) {
	reportURL := os.Getenv("REPORT_WEBHOOK_URL")

	// format file to be downloadable url
	//
	fileName := strings.ReplaceAll(file, "downloads/", "download/")
	url := fmt.Sprintf("%s/%s", os.Getenv("DOWNLOAD_BASE_URL"), fileName)

	payload := ReportPayload{
		ID:            id,
		URL:           url,
		WebhookParams: webHookParams,
	}

	log.Printf("Reporting download success for ID: %s, url: %s", id, url)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal report payload: %s", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, reportURL, bytes.NewBuffer(data))
	if err != nil {
		log.Printf("Failed to create report request: %s", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send report request: %s", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Report request failed with status: %d", resp.StatusCode)
	}
}

func reportDownloadFailed(id string, webHookParams WebhookParams, err error) {
	reportURL := os.Getenv("REPORT_WEBHOOK_URL")

	payload := ReportPayload{
		ID:            id,
		URL:           "",
		WebhookParams: webHookParams,
		Error:         err.Error(),
	}

	log.Printf("Reporting download failure for ID: %s, error: %s", id, err)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal report payload: %s", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, reportURL, bytes.NewBuffer(data))
	if err != nil {
		log.Printf("Failed to create report request: %s", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send report request: %s", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Report request failed with status: %d", resp.StatusCode)
	}
}

func requestToDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var req WebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.URL == "" || req.ID == "" {
		http.Error(w, "Missing 'url' or 'id' in payload", http.StatusBadRequest)
		return
	}

	go func(url, id string, webhookParams WebhookParams) {
		file, err := downloadVideo(url, id)
		if err != nil {
			log.Printf("Failed to download video: %s", err)
			reportDownloadFailed(id, webhookParams, err)
			return
		}

		log.Printf("Downloaded video for ID: %s, file: %s", id, file)

		reportDownloadSuccess(id, file, webhookParams)
	}(req.URL, req.ID, req.WebhookParams)

	resp := WebhookResponse{
		Message: "Download started successfully",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}

func downloadFileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	fileName := strings.TrimPrefix(r.URL.Path, "/download/")
	if fileName == "" {
		http.Error(w, "Missing file name in path", http.StatusBadRequest)
		return
	}
	if strings.Contains(fileName, "..") {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	filePath := filepath.Join("downloads", fileName)
	if !strings.HasPrefix(filepath.Clean(filePath), filepath.Clean("downloads")+string(os.PathSeparator)) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filePath)
}

func main() {
	fmt.Println("Assetor v0.0.7")
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Ensure downloads directory exists
	if err := os.MkdirAll("downloads", os.ModePerm); err != nil {
		log.Fatalf("Failed to create downloads directory: %s", err)
	}

	http.HandleFunc("/pull", requestToDownloadHandler)
	http.HandleFunc("/download/", downloadFileHandler)

	port := ":4412"
	log.Printf("Starting server on port %s", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %s", err)
	}
}
